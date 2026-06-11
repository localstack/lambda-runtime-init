// main entrypoint of init
// initial structure based upon /cmd/aws-lambda-rie/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/fatalerror"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore"
	log "github.com/sirupsen/logrus"
)

const (
	// defaultInitPhaseTimeoutSeconds matches AWS's 10s init-phase limit. When init exceeds
	// this, the init is retried at the time of the first invocation under the function
	// timeout ("suppressed init"). Override via LOCALSTACK_INIT_PHASE_TIMEOUT.
	defaultInitPhaseTimeoutSeconds = 10
	// initResetTimeoutMs bounds the reset that aborts a timed-out init so rapidcore re-runs
	// it on the first invocation.
	initResetTimeoutMs = 2000
)

type LsOpts struct {
	InteropPort         string
	RuntimeEndpoint     string
	RuntimeId           string
	AccountId           string
	InitTracingPort     string
	User                string
	CodeArchives        string
	HotReloadingPaths   []string
	FileWatcherStrategy string
	ChmodPaths          string
	LocalstackIP        string
	InitLogLevel        string
	EdgePort            string
	EnableXRayTelemetry string
	PostInvokeWaitMS    string
	MaxPayloadSize      string
	InitPhaseTimeout    string
}

func GetEnvOrDie(env string) string {
	result, found := os.LookupEnv(env)
	if !found {
		panic("Could not find environment variable for: " + env)
	}
	return result
}

func InitLsOpts() *LsOpts {
	return &LsOpts{
		// required
		RuntimeEndpoint: GetEnvOrDie("LOCALSTACK_RUNTIME_ENDPOINT"),
		RuntimeId:       GetEnvOrDie("LOCALSTACK_RUNTIME_ID"),
		AccountId:       GetenvWithDefault("LOCALSTACK_FUNCTION_ACCOUNT_ID", "000000000000"),
		// optional with default
		InteropPort:      GetenvWithDefault("LOCALSTACK_INTEROP_PORT", "9563"),
		InitTracingPort:  GetenvWithDefault("LOCALSTACK_RUNTIME_TRACING_PORT", "9564"),
		User:             GetenvWithDefault("LOCALSTACK_USER", "sbx_user1051"),
		InitLogLevel:     GetenvWithDefault("LOCALSTACK_INIT_LOG_LEVEL", "warn"),
		EdgePort:         GetenvWithDefault("EDGE_PORT", "4566"),
		MaxPayloadSize:   GetenvWithDefault("LOCALSTACK_MAX_PAYLOAD_SIZE", "6291556"),
		InitPhaseTimeout: GetenvWithDefault("LOCALSTACK_INIT_PHASE_TIMEOUT", strconv.Itoa(defaultInitPhaseTimeoutSeconds)),
		// optional or empty
		CodeArchives:        os.Getenv("LOCALSTACK_CODE_ARCHIVES"),
		HotReloadingPaths:   strings.Split(GetenvWithDefault("LOCALSTACK_HOT_RELOADING_PATHS", ""), ","),
		FileWatcherStrategy: os.Getenv("LOCALSTACK_FILE_WATCHER_STRATEGY"),
		EnableXRayTelemetry: os.Getenv("LOCALSTACK_ENABLE_XRAY_TELEMETRY"),
		LocalstackIP:        os.Getenv("LOCALSTACK_HOSTNAME"),
		PostInvokeWaitMS:    os.Getenv("LOCALSTACK_POST_INVOKE_WAIT_MS"),
		ChmodPaths:          GetenvWithDefault("LOCALSTACK_CHMOD_PATHS", "[]"),
	}
}

// UnsetLsEnvs unsets environment variables specific to LocalStack to achieve better runtime parity with AWS
func UnsetLsEnvs() {
	unsetList := [...]string{
		// LocalStack internal
		"LOCALSTACK_RUNTIME_ENDPOINT",
		"LOCALSTACK_RUNTIME_ID",
		"LOCALSTACK_INTEROP_PORT",
		"LOCALSTACK_RUNTIME_TRACING_PORT",
		"LOCALSTACK_USER",
		"LOCALSTACK_CODE_ARCHIVES",
		"LOCALSTACK_HOT_RELOADING_PATHS",
		"LOCALSTACK_ENABLE_XRAY_TELEMETRY",
		"LOCALSTACK_INIT_LOG_LEVEL",
		"LOCALSTACK_POST_INVOKE_WAIT_MS",
		"LOCALSTACK_FUNCTION_ACCOUNT_ID",
		"LOCALSTACK_MAX_PAYLOAD_SIZE",
		"LOCALSTACK_INIT_PHASE_TIMEOUT",
		"LOCALSTACK_CHMOD_PATHS",

		// Docker container ID
		"HOSTNAME",
		// User
		"HOME",
	}
	for _, envKey := range unsetList {
		if err := os.Unsetenv(envKey); err != nil {
			log.Warnln("Could not unset environment variable:", envKey, err)
		}
	}
}

func main() {
	// we're setting this to the same value as in the official RIE
	debug.SetGCPercent(33)

	// configuration parsing
	lsOpts := InitLsOpts()
	UnsetLsEnvs()

	// set up logging following the Logrus logging levels: https://github.com/sirupsen/logrus#level-logging
	log.SetReportCaller(true)
	// https://docs.aws.amazon.com/xray/latest/devguide/xray-daemon-configuration.html
	xRayLogLevel := "info"
	switch lsOpts.InitLogLevel {
	case "trace":
		log.SetFormatter(&log.JSONFormatter{})
		log.SetLevel(log.TraceLevel)
		xRayLogLevel = "debug"
	case "debug":
		log.SetLevel(log.DebugLevel)
		xRayLogLevel = "debug"
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
		xRayLogLevel = "warn"
	case "error":
		log.SetLevel(log.ErrorLevel)
		xRayLogLevel = "error"
	case "fatal":
		log.SetLevel(log.FatalLevel)
		xRayLogLevel = "error"
	case "panic":
		log.SetLevel(log.PanicLevel)
		xRayLogLevel = "error"
	default:
		log.Fatal("Invalid value for LOCALSTACK_INIT_LOG_LEVEL")
	}

	// patch MaxPayloadSize
	payloadSize, err := strconv.Atoi(lsOpts.MaxPayloadSize)
	if err != nil {
		log.Panicln("Please specify a number for LOCALSTACK_MAX_PAYLOAD_SIZE")
	}
	interop.MaxPayloadSize = payloadSize

	// download code archive if env variable is set
	if err := DownloadCodeArchives(lsOpts.CodeArchives); err != nil {
		log.Fatal("Failed to download code archives: " + err.Error())
	}

	if err := AdaptFilesystemPermissions(lsOpts.ChmodPaths); err != nil {
		log.Warnln("Could not change file mode of code directories:", err)
	}

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

	EnsureHome()

	// file watcher for hot-reloading
	fileWatcherContext, cancelFileWatcher := context.WithCancel(context.Background())

	logCollector := NewLogCollector()
	localStackLogsEgressApi := NewLocalStackLogsEgressAPI(logCollector)
	tracer := NewLocalStackTracer()

	// Create LocalStack adapter upfront so it can be shared with the interop server
	lsAdapter := &LocalStackAdapter{
		UpstreamEndpoint: lsOpts.RuntimeEndpoint,
		RuntimeId:        lsOpts.RuntimeId,
	}

	// Events API rides rapidcore's invoke lifecycle to emit the synthetic START log line after
	// any inline (suppressed) init, matching AWS's ordering.
	lsEventsAPI := NewLocalStackEventsAPI(logCollector)

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
		SetTracer(tracer).
		SetEventsAPI(lsEventsAPI)

	// Corresponds to the 'AWS_LAMBDA_RUNTIME_API' environment variable.
	// We need to ensure the runtime server is up before the INIT phase,
	// but this envar is only set after the InitHandler is called.
	runtimeAPIAddress := "127.0.0.1:9001"
	sandbox.SetRuntimeAPIAddress(runtimeAPIAddress)

	// xray daemon
	endpoint := "http://" + lsOpts.LocalstackIP + ":" + lsOpts.EdgePort
	xrayConfig := initConfig(endpoint, xRayLogLevel)
	d := initDaemon(xrayConfig, lsOpts.EnableXRayTelemetry == "1")
	sandbox.AddShutdownFunc(func() {
		log.Debugln("Shutting down xray daemon")
		d.stop()
		log.Debugln("Flushing segments in xray daemon")
		d.close()
	})
	runDaemon(d) // async

	defaultInterop := sandbox.DefaultInteropServer()
	interopServer := NewCustomInteropServer(lsOpts, lsAdapter, defaultInterop, logCollector, lsEventsAPI)
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

	initPhaseTimeoutSeconds := defaultInitPhaseTimeoutSeconds
	if parsed, perr := strconv.Atoi(lsOpts.InitPhaseTimeout); perr == nil {
		initPhaseTimeoutSeconds = parsed
	} else {
		log.Warnln("Invalid LOCALSTACK_INIT_PHASE_TIMEOUT, using default:", perr)
	}

	log.Debugln("Awaiting initialization of runtime init.")
	initResp, timedOut, err := interopServer.delegate.AwaitInitializedWithTimeout(
		time.Duration(initPhaseTimeoutSeconds) * time.Second,
	)
	switch {
	case timedOut && !interopServer.onDemand:
		// Provisioned concurrency / Managed Instances: AWS fails the provisioning operation
		// when the extended init window is exceeded — there is no suppressed-init retry at
		// invoke time. Report the failure and exit instead of signaling ready.
		// TODO: validate the exact provisioning-failure errorType/message against AWS
		// (e.g. the Managed Instances API model's FUNCTION_ERROR_INIT_TIMEOUT).
		log.Errorf("Extended init phase timed out after %ds. Exiting.", initPhaseTimeoutSeconds)
		interopServer.SendInitError(
			fatalerror.SandboxTimeout,
			fmt.Errorf("Init phase timed out after %d seconds", initPhaseTimeoutSeconds),
		)
		return
	case timedOut:
		// On-demand: AWS limits the init phase to 10s. When exceeded, init is retried at the
		// time of the first invocation under the function timeout ("suppressed init"). We
		// report the init timeout, reset the in-progress init so rapidcore re-runs a fresh
		// Init phase when the first invoke arrives, and only then signal ready.
		// The reset must complete BEFORE signaling ready: its cleanup (Clear/Release in
		// rapidcore.Server.Reset) releases the current reservation, so running it concurrently
		// with the first invoke's Reserve() would cancel that invoke's reservation mid-flight.
		// The reset cannot block on the unconsumed init failure: awaitInitCompletion acks rapid
		// before the (still pending) initFailures channel send, which the invoke path's
		// Reserve()/awaitInitialized() later consumes to trigger the suppressed init.
		log.Debugln("Init phase timed out; deferring to suppressed init on first invocation.")
		interopServer.ReportInitTimeout()
		if _, resetErr := interopServer.delegate.Reset("initTimeout", initResetTimeoutMs); resetErr != nil {
			// A non-nil error only carries the aborted init's fatal error type; the reset
			// itself has completed and the suppressed-init retry stays valid.
			log.Debugf("Reset after init timeout returned: %s", resetErr)
		}
		// Consume the reset-interrupted init's failure notification: if the first invoke's
		// awaitInitialized() consumed it instead, rapidcore would cache a generic placeholder
		// error (Sandbox.Failure with an empty payload) that masks the real error when the
		// suppressed init re-run fails (e.g. a runtime crash without /init/error).
		interopServer.delegate.DrainInitFailure()
	case interopServer.onDemand && errors.Is(err, rapidcore.ErrInitDoneFailed):
		// On-demand: AWS folds a failed cold-start init into the first invocation (suppressed
		// init). Signal ready and keep the process alive so LocalStack dispatches the first
		// invoke, which surfaces the cached init error (or a runtime-exit error) together with
		// the full INIT_REPORT/START/END/REPORT log envelope. SendInitErrorResponse has already
		// cached the structured error (without reporting via /status/error for on-demand).
		// Record the failure type detected by rapidcore so runtimes that crashed WITHOUT
		// calling /init/error still get the error envelope (no-op when /init/error already
		// recorded the runtime-reported type).
		log.Debugln("Init failed; deferring to first invocation (on-demand suppressed init).")
		interopServer.RecordInitError(initResp.InitErrorType)
	case err != nil:
		// PC/SnapStart/MI, or an init-phase reset: report the failure now and exit. When the
		// runtime reported its own error via /init/error, SendInitErrorResponse already
		// forwarded it and SendInitError is a no-op. When the runtime crashed/exited without
		// reporting, this is the only callback that notifies LocalStack.
		log.Errorln("Runtime init failed to initialize: " + err.Error() + ". Exiting.")
		if !errors.Is(err, rapidcore.ErrInitResetReceived) {
			interopServer.SendInitError(initResp.InitErrorType, initResp.InitErrorMessage)
		}
		return
	}

	log.Debugln("Completed initialization of runtime init. Sending status ready to LocalStack.")
	if err := interopServer.localStackAdapter.SendStatus(Ready, []byte{}); err != nil {
		log.Fatalln("Failed to send status ready to LocalStack " + err.Error() + ". Exiting.")
	}

	<-exitChan
}
