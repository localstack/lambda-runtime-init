// main entrypoint of init
// initial structure based upon /cmd/aws-lambda-rie/main.go
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/localstack/lambda-runtime-init/internal/aws/lambda"
	"github.com/localstack/lambda-runtime-init/internal/aws/xray"
	"github.com/localstack/lambda-runtime-init/internal/bootstrap"
	"github.com/localstack/lambda-runtime-init/internal/events"
	"github.com/localstack/lambda-runtime-init/internal/hotreloading"
	"github.com/localstack/lambda-runtime-init/internal/localstack"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	"github.com/localstack/lambda-runtime-init/internal/sandbox"

	"github.com/localstack/lambda-runtime-init/internal/server"

	"github.com/localstack/lambda-runtime-init/internal/supervisor"
	"github.com/localstack/lambda-runtime-init/internal/tracing"
	"github.com/localstack/lambda-runtime-init/internal/utils"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/directinvoke"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
)

func InitLsOpts() *localstack.Config {
	return &localstack.Config{
		// required
		RuntimeEndpoint: utils.MustGetEnv("LOCALSTACK_RUNTIME_ENDPOINT"),
		RuntimeId:       utils.MustGetEnv("LOCALSTACK_RUNTIME_ID"),
		AccountId:       utils.GetEnvWithDefault("LOCALSTACK_FUNCTION_ACCOUNT_ID", "000000000000"),
		// optional with default
		InteropPort:     utils.GetEnvWithDefault("LOCALSTACK_INTEROP_PORT", "9563"),
		InitTracingPort: utils.GetEnvWithDefault("LOCALSTACK_RUNTIME_TRACING_PORT", "9564"),
		User:            utils.GetEnvWithDefault("LOCALSTACK_USER", "sbx_user1051"),
		InitLogLevel:    utils.GetEnvWithDefault("LOCALSTACK_INIT_LOG_LEVEL", "warn"),
		EdgePort:        utils.GetEnvWithDefault("EDGE_PORT", "4566"),
		MaxPayloadSize:  utils.GetEnvWithDefault("LOCALSTACK_MAX_PAYLOAD_SIZE", "6291556"),
		// optional or empty
		CodeArchives:        os.Getenv("LOCALSTACK_CODE_ARCHIVES"),
		HotReloadingPaths:   strings.Split(utils.GetEnvWithDefault("LOCALSTACK_HOT_RELOADING_PATHS", ""), ","),
		FileWatcherStrategy: os.Getenv("LOCALSTACK_FILE_WATCHER_STRATEGY"),
		EnableXRayTelemetry: os.Getenv("LOCALSTACK_ENABLE_XRAY_TELEMETRY"),
		LocalstackIP:        os.Getenv("LOCALSTACK_HOSTNAME"),
		PostInvokeWaitMS:    os.Getenv("LOCALSTACK_POST_INVOKE_WAIT_MS"),
		ChmodPaths:          utils.GetEnvWithDefault("LOCALSTACK_CHMOD_PATHS", "[]"),
	}
}

func InitFunctionConfig() lambda.FunctionConfig {
	return lambda.FunctionConfig{
		FunctionName:         utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_NAME", "test_function"),
		FunctionVersion:      utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_VERSION", "$LATEST"),
		FunctionTimeoutSec:   utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_TIMEOUT", "30"),
		InitializationType:   utils.GetEnvWithDefault("AWS_LAMBDA_INITIALIZATION_TYPE", "on-demand"),
		LogGroupName:         utils.GetEnvWithDefault("AWS_LAMBDA_LOG_GROUP_NAME", "/aws/lambda/Functions"),
		LogStreamName:        utils.GetEnvWithDefault("AWS_LAMBDA_LOG_STREAM_NAME", "$LATEST"),
		FunctionMemorySizeMb: utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "128"),
		FunctionHandler:      utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_HANDLER", os.Getenv("_HANDLER")),
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

func setupUserPermissions(user string) {
	logger := utils.UserLogger()
	// Switch to non-root user and drop root privileges
	if utils.IsRootUser() && user != "" && user != "root" {
		uid := 993
		gid := 990
		utils.AddUser(user, uid, gid)
		if err := os.Chown("/tmp", uid, gid); err != nil {
			log.WithError(err).Warn("Could not change owner of directory /tmp.")
		}
		logger.Debug("Process running as root user.")
		err := utils.DropPrivileges(user)
		if err != nil {
			log.WithError(err).Warn("Could not drop root privileges.")
		} else {
			logger.Debug("Process running as non-root user.")
		}
	}
}

func main() {
	// we're setting this to the same value as in the official RIE
	debug.SetGCPercent(33)

	// configuration parsing
	lsOpts := InitLsOpts()
	functionConf := InitFunctionConfig()
	awsEnvConf, _ := config.NewEnvConfig()
	awsEnvConf.Credentials.AccountID = lsOpts.AccountId

	UnsetLsEnvs()

	// set up logging following the Logrus logging levels: https://github.com/sirupsen/logrus#level-logging
	log.SetReportCaller(true)
	// https://docs.aws.amazon.com/xray/latest/devguide/xray-daemon-configuration.html

	logLevel, err := log.ParseLevel(lsOpts.InitLogLevel)
	if err != nil {
		log.Fatal("Invalid value for LOCALSTACK_INIT_LOG_LEVEL")
	}
	log.SetLevel(logLevel)

	xRayLogLevel := lsOpts.InitLogLevel
	switch logLevel {
	case log.TraceLevel:
		log.SetFormatter(&log.JSONFormatter{})
	case log.ErrorLevel, log.FatalLevel, log.PanicLevel:
		xRayLogLevel = "error"
	}

	// patch MaxPayloadSize
	payloadSize, err := strconv.Atoi(lsOpts.MaxPayloadSize)
	if err != nil {
		log.Panicln("Please specify a number for LOCALSTACK_MAX_PAYLOAD_SIZE")
	}
	directinvoke.MaxDirectResponseSize = int64(payloadSize)
	if directinvoke.MaxDirectResponseSize > interop.MaxPayloadSize {
		log.Infof("Large response size detected (%d bytes), forcing streaming mode", directinvoke.MaxDirectResponseSize)
		directinvoke.InvokeResponseMode = interop.InvokeResponseModeStreaming
	}

	// download code archive if env variable is set
	if err := utils.DownloadCodeArchives(lsOpts.CodeArchives); err != nil {
		log.Fatal("Failed to download code archives: " + err.Error())
	}

	if err := utils.AdaptFilesystemPermissions(lsOpts.ChmodPaths); err != nil {
		log.Warnln("Could not change file mode of code directories:", err)
	}

	// parse CLI args
	bootstrap, handler := bootstrap.GetBootstrap(os.Args)

	// Switch to non-root user and drop root privileges
	setupUserPermissions(lsOpts.User)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// LocalStack client used for sending callbacks
	lsClient := localstack.NewLocalStackClient(lsOpts.RuntimeEndpoint, lsOpts.RuntimeId)

	// Services required for Sandbox environment
	interopServer := server.NewInteropServer(lsClient)

	logCollector := logging.NewLogCollector()
	localStackLogsEgressApi := logging.NewLocalStackLogsEgressAPI(logCollector)
	tracer := tracing.NewLocalStackTracer()
	lsEventsAPI := events.NewLocalStackEventsAPI(lsClient)
	localStackSupv := supervisor.NewLocalStackSupervisor(ctx, lsEventsAPI)

	// build sandbox
	builder := rapidcore.
		NewSandboxBuilder().
		SetTracer(tracer).
		SetEventsAPI(lsEventsAPI).
		SetHandler(handler).
		SetInteropServer(interopServer).
		SetLogsEgressAPI(localStackLogsEgressApi).
		SetRuntimeAPIAddress("127.0.0.1:9000").
		SetSupervisor(localStackSupv).
		SetRuntimeFsRootPath("/")

	builder.AddShutdownFunc(func() {
		interopServer.Close()
	})

	// Start daemons

	// file watcher for hot-reloading
	fileWatcherContext, cancelFileWatcher := context.WithCancel(ctx)
	builder.AddShutdownFunc(func() {
		cancelFileWatcher()
	})

	go hotreloading.RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext, lsOpts.FileWatcherStrategy)

	// Start xray daemon
	endpoint := "http://" + net.JoinHostPort(lsOpts.LocalstackIP, lsOpts.EdgePort)
	xrayConfig := xray.NewConfig(endpoint, xRayLogLevel)
	d := xray.NewDaemon(xrayConfig, lsOpts.EnableXRayTelemetry == "1")
	builder.AddShutdownFunc(func() {
		log.Debugln("Shutting down xray daemon")
		d.Stop()
		log.Debugln("Flushing segments in xray daemon")
		d.Close()
	})
	d.Run() // served async

	// Populate our interop server
	sandboxCtx, internalStateFn := builder.Create()

	interopServer.SetSandboxContext(sandboxCtx)
	interopServer.SetInternalStateGetter(internalStateFn)

	// TODO(gregfurman): The default interops server has a shutdown function added in the builder.
	// This calls sandbox.Reset(), where if sandbox is not set, this will panic.
	// So instead, just set the sandbox to a noop version.
	builder.DefaultInteropServer().SetSandboxContext(sandbox.NewNoopSandboxContext())

	// Create the LocalStack service
	localStackService := server.NewLocalStackService(
		interopServer, logCollector, lsClient, localStackSupv, xrayConfig.Endpoint, lsOpts, functionConf, awsEnvConf,
	)
	defer localStackService.Close()

	// start runtime init. It is important to start `InitHandler` synchronously because we need to ensure the
	// notification channels and status fields are properly initialized before `AwaitInitialized`
	log.Debugln("Starting runtime init.")
	if err := localStackService.Initialize(bootstrap); err != nil {
		log.WithError(err).Warnf("Failed to initialize runtime. Initialization will be retried in the next invoke.")
	}

	invokeServer := server.NewServer(lsOpts.InteropPort, localStackService)
	defer invokeServer.Close()

	serverErr := make(chan error, 1)
	go func() {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%s", lsOpts.InteropPort))
		if err != nil {
			log.Fatalf("failed to start LocalStack Lambda Runtime Interface server: %s", err)
		}
		go func() { serverErr <- invokeServer.Serve(listener); close(serverErr) }()
		log.Debugf("LocalStack API gateway listening on %s", listener.Addr().String())
	}()

	log.Debugln("Awaiting initialization of runtime init.")
	if err := interopServer.AwaitInitialized(); err != nil {
		// Error cases: ErrInitDoneFailed or ErrInitResetReceived
		log.Errorln("Runtime init failed to initialize: " + err.Error() + ". Exiting.")
		// NOTE: Sending the error status to LocalStack is handled beforehand in the custom_interop.go through the
		// callback SendInitErrorResponse because it contains the correct error response payload.
		// return
	} else {
		log.Debugln("Completed initialization of runtime init. Sending status ready to LocalStack.")
		if err := localStackService.SendStatus(localstack.Ready, []byte{}); err != nil {
			log.Fatalln("Failed to send status ready to LocalStack " + err.Error() + ". Exiting.")
		}
	}

	// Block until context is cancelled OR the server errors out
	select {
	case <-ctx.Done():
		log.Info("Shutdown signal received.")
	case <-serverErr:
		if err != nil {
			log.Errorf("Server error: %v", err)
		}
	}
}
