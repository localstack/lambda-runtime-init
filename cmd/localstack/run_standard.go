package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore"
	log "github.com/sirupsen/logrus"
)

func runStandard(lsOpts *LsOpts) {
	configureStandardLogger(lsOpts.InitLogLevel)

	// xRayLogLevel := getXRayLogLevel(lsOpts.InitLogLevel)

	payloadSize, err := strconv.Atoi(lsOpts.MaxPayloadSize)
	if err != nil {
		log.Panicln("Please specify a number for LOCALSTACK_MAX_PAYLOAD_SIZE")
	}
	interop.MaxPayloadSize = payloadSize

	// parse CLI args
	bootstrap, handler := getBootstrap(os.Args)

	// Switch to non-root user and drop root privileges
	if IsRootUser() && lsOpts.User != "" && lsOpts.User != "root" {
		uid := 993
		gid := 990
		AddUser(lsOpts.User, uid, gid)
		if err := os.Chown("/tmp", uid, gid); err != nil {
			log.Warnln("Could not change owner of directory /tmp:", err)
		}
		UserLogger().Debugln("Process running as root user.")
		err := DropPrivileges(lsOpts.User)
		if err != nil {
			log.Warnln("Could not drop root privileges.", err)
		} else {
			UserLogger().Debugln("Process running as non-root user.")
		}
	}

	// file watcher for hot-reloading
	fileWatcherContext, cancelFileWatcher := context.WithCancel(context.Background())

	logCollector := NewLogCollector()
	localStackLogsEgressApi := NewLocalStackLogsEgressAPI(logCollector)
	tracer := NewLocalStackTracer()

	// build sandbox
	sandbox := rapidcore.
		NewSandboxBuilder().
		//SetTracer(tracer).
		AddShutdownFunc(func() {
			log.Debugln("Stopping file watcher")
			cancelFileWatcher()
		}).
		SetExtensionsFlag(true).
		SetInitCachingFlag(true).
		SetLogsEgressAPI(localStackLogsEgressApi).
		SetTracer(tracer)

	// Corresponds to the 'AWS_LAMBDA_RUNTIME_API' environment variable.
	// We need to ensure the runtime server is up before the INIT phase,
	// but this envar is only set after the InitHandler is called.
	runtimeAPIAddress := "127.0.0.1:9001"
	sandbox.SetRuntimeAPIAddress(runtimeAPIAddress)

	// Initialize X-Ray daemon
	d := doInitDaemon(
		lsOpts.LocalstackIP,
		lsOpts.EdgePort,
		lsOpts.EnableXRayTelemetry == "1",
		lsOpts.InitLogLevel,
	)

	sandbox.AddShutdownFunc(func() {
		log.Debugln("Shutting down xray daemon")
		d.stop()
		log.Debugln("Flushing segments in xray daemon")
		d.close()
	})

	defaultInterop := sandbox.DefaultInteropServer()
	interopServer := NewCustomInteropServer(lsOpts, defaultInterop, logCollector)
	sandbox.SetInteropServer(interopServer)
	if len(handler) > 0 {
		sandbox.SetHandler(handler)
	}
	exitChan := make(chan struct{})
	sandbox.AddShutdownFunc(func() {
		exitChan <- struct{}{}
	})

	// initialize all flows and start runtime API
	sandboxContext, internalStateFn := sandbox.Create()
	// Populate our custom interop server
	interopServer.SetSandboxContext(sandboxContext)
	interopServer.SetInternalStateGetter(internalStateFn)

	// get timeout
	invokeTimeoutEnv := GetEnvOrDie("AWS_LAMBDA_FUNCTION_TIMEOUT") // TODO: collect all AWS_* env parsing
	invokeTimeoutSeconds, err := strconv.Atoi(invokeTimeoutEnv)
	if err != nil {
		log.Fatalln(err)
	}
	go RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext, lsOpts.FileWatcherStrategy)

	log.Debugf("Awaiting initialization of runtime api at %s.", runtimeAPIAddress)
	// Fixes https://github.com/localstack/localstack/issues/12680
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := waitForRuntimeAPI(ctx, runtimeAPIAddress); err != nil {
		log.Fatalf("Lambda Runtime API server at %s did not come up in 30s, with error %s", runtimeAPIAddress, err.Error())
	}
	cancel()

	// start runtime init. It is important to start `InitHandler` synchronously because we need to ensure the
	// notification channels and status fields are properly initialized before `AwaitInitialized`
	log.Debugln("Starting runtime init.")
	InitHandler(sandbox.LambdaInvokeAPI(), GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION"), int64(invokeTimeoutSeconds), bootstrap, lsOpts.AccountId) // TODO: replace this with a custom init

	log.Debugln("Awaiting initialization of runtime init.")
	if err := interopServer.delegate.AwaitInitialized(); err != nil {
		// Error cases: ErrInitDoneFailed or ErrInitResetReceived
		log.Errorln("Runtime init failed to initialize: " + err.Error() + ". Exiting.")
		// NOTE: Sending the error status to LocalStack is handled beforehand in the custom_interop.go through the
		// callback SendInitErrorResponse because it contains the correct error response payload.
		return
	}

	log.Debugln("Completed initialization of runtime init. Sending status ready to LocalStack.")
	if err := interopServer.localStackAdapter.SendStatus(Ready, []byte{}); err != nil {
		log.Fatalln("Failed to send status ready to LocalStack " + err.Error() + ". Exiting.")
	}

	<-exitChan
}
