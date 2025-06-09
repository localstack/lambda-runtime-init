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
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/localstack/lambda-runtime-init/internal/aws/lambda"
	"github.com/localstack/lambda-runtime-init/internal/aws/xray"
	"github.com/localstack/lambda-runtime-init/internal/bootstrap"
	"github.com/localstack/lambda-runtime-init/internal/events"
	"github.com/localstack/lambda-runtime-init/internal/hotreloading"
	"github.com/localstack/lambda-runtime-init/internal/localstack"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	"github.com/localstack/lambda-runtime-init/internal/server"

	"github.com/localstack/lambda-runtime-init/internal/supervisor"
	"github.com/localstack/lambda-runtime-init/internal/tracing"
	"github.com/localstack/lambda-runtime-init/internal/utils"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/directinvoke"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
	supv "go.amzn.com/lambda/supervisor"
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
		FunctionMemorySizeMb: utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "3008"),
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
	if utils.IsRootUser() && lsOpts.User != "" && lsOpts.User != "root" {
		uid := 993
		gid := 990
		utils.AddUser(lsOpts.User, uid, gid)
		if err := os.Chown("/tmp", uid, gid); err != nil {
			log.Warnln("Could not change owner of directory /tmp:", err)
		}
		utils.UserLogger().Debugln("Process running as root user.")
		err := utils.DropPrivileges(lsOpts.User)
		if err != nil {
			log.Warnln("Could not drop root privileges.", err)
		} else {
			utils.UserLogger().Debugln("Process running as non-root user.")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// file watcher for hot-reloading
	fileWatcherContext, cancelFileWatcher := context.WithCancel(ctx)
	defer cancelFileWatcher()

	// Custom Interop Server
	defaultServer := rapidcore.NewServer()
	lsClient := localstack.NewLocalStackClient(lsOpts.RuntimeEndpoint, lsOpts.RuntimeId)
	interopServer := server.NewInteropServer(defaultServer, lsClient)

	// Services required for Sandbox environment
	logCollector := logging.NewLogCollector()
	localStackLogsEgressApi := logging.NewLocalStackLogsEgressAPI(logCollector)
	tracer := tracing.NewLocalStackTracer()
	eventsListener := events.NewLocalStackEventsAPI(lsClient)

	defaultSupv := supv.NewLocalSupervisor()
	localStackSupv := supervisor.NewLocalStackSupervisor(ctx, defaultSupv, eventsListener)

	// build sandbox
	exitChan := make(chan struct{})
	sandbox := rapidcore.
		NewSandboxBuilder().
		AddShutdownFunc(func() {
			log.Debugln("Stopping file watcher")
			cancelFileWatcher()
		}).
		AddShutdownFunc(func() {
			exitChan <- struct{}{}
		}).
		SetExtensionsFlag(true).
		SetInitCachingFlag(true).
		SetLogsEgressAPI(localStackLogsEgressApi).
		SetTracer(tracer).
		SetInteropServer(interopServer).
		SetSupervisor(localStackSupv).
		SetHandler(handler)

	// Start daemons

	// Start hot-reloading watcher
	go hotreloading.RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext, lsOpts.FileWatcherStrategy)

	// xray daemon
	endpoint := "http://" + net.JoinHostPort(lsOpts.LocalstackIP, lsOpts.EdgePort)
	xrayConfig := xray.NewConfig(endpoint, xRayLogLevel)
	d := xray.NewDaemon(xrayConfig, lsOpts.EnableXRayTelemetry == "1")
	defer func() {
		log.Debugln("Shutting down xray daemon")
		d.Stop()
		log.Debugln("Flushing segments in xray daemon")
		d.Close()
	}()
	d.Run() // served async

	// initialize all flows and start runtime API
	sandboxContext, internalStateFn := sandbox.Create()
	// Populate our interop server
	interopServer.SetSandboxContext(sandboxContext)
	interopServer.SetInternalStateGetter(internalStateFn)

	localStackService := server.NewLocalStackService(
		interopServer, logCollector, lsClient, localStackSupv, xrayConfig.Endpoint, lsOpts, functionConf, awsEnvConf,
	)

	// start runtime init. It is important to start `InitHandler` synchronously because we need to ensure the
	// notification channels and status fields are properly initialized before `AwaitInitialized`
	log.Debugln("Starting runtime init.")
	if err := localStackService.Initialize(bootstrap); err != nil {
		log.WithError(err).Warnf("Failed to initialize runtime. Initialization will be retried in the next invoke.")
	}

	invokeServer := server.NewServer(lsOpts.InteropPort, localStackService)
	invokeServer.RegisterOnShutdown(localStackService.Close)

	defer invokeServer.Shutdown(context.Background())

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		listener, err := net.Listen("tcp", fmt.Sprintf(":%s", lsOpts.InteropPort))

		if err != nil {
			log.Fatalf("failed to start listener for custom interops server: %s", err)
		}
		go invokeServer.Serve(listener)
		log.Debugf("LocalStack API gateway listening on %s", listener.Addr().String())
	}()

	wg.Wait()

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

	select {
	case <-ctx.Done():
	case <-exitChan:
	}

	gracefulCtx, cancel := context.WithTimeout(ctx, time.Millisecond*500)
	defer cancel()

	if err := localStackService.AwaitCompleted(gracefulCtx); err != nil {
		log.Warnf("Did not gracefully complete: %w", err)
	}

}
