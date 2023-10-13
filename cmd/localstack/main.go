// main entrypoint of init
// initial structure based upon /cmd/aws-lambda-rie/main.go
package main

import (
	"context"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/rapidcore"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

type LsOpts struct {
	InteropPort         string
	RuntimeEndpoint     string
	RuntimeId           string
	InitTracingPort     string
	User                string
	CodeArchives        string
	HotReloadingPaths   []string
	EnableDnsServer     string
	LocalstackIP        string
	InitLogLevel        string
	EdgePort            string
	EnableXRayTelemetry string
	PostInvokeWaitMS    string
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
		// optional with default
		InteropPort:     GetenvWithDefault("LOCALSTACK_INTEROP_PORT", "9563"),
		InitTracingPort: GetenvWithDefault("LOCALSTACK_RUNTIME_TRACING_PORT", "9564"),
		User:            GetenvWithDefault("LOCALSTACK_USER", "sbx_user1051"),
		InitLogLevel:    GetenvWithDefault("LOCALSTACK_INIT_LOG_LEVEL", "warn"),
		EdgePort:        GetenvWithDefault("EDGE_PORT", "4566"),
		// optional or empty
		CodeArchives:        os.Getenv("LOCALSTACK_CODE_ARCHIVES"),
		HotReloadingPaths:   strings.Split(GetenvWithDefault("LOCALSTACK_HOT_RELOADING_PATHS", ""), ","),
		EnableDnsServer:     os.Getenv("LOCALSTACK_ENABLE_DNS_SERVER"),
		EnableXRayTelemetry: os.Getenv("LOCALSTACK_ENABLE_XRAY_TELEMETRY"),
		LocalstackIP:        os.Getenv("LOCALSTACK_HOSTNAME"),
		PostInvokeWaitMS:    os.Getenv("LOCALSTACK_POST_INVOKE_WAIT_MS"),
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
		"LOCALSTACK_ENABLE_DNS_SERVER",
		"LOCALSTACK_ENABLE_XRAY_TELEMETRY",
		"LOCALSTACK_INIT_LOG_LEVEL",
		"LOCALSTACK_POST_INVOKE_WAIT_MS",

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

	// enable dns server
	dnsServerContext, stopDnsServer := context.WithCancel(context.Background())
	go RunDNSRewriter(lsOpts, dnsServerContext)

	// download code archive if env variable is set
	if err := DownloadCodeArchives(lsOpts.CodeArchives); err != nil {
		log.Fatal("Failed to download code archives: " + err.Error())
	}

	// parse CLI args
	bootstrap, handler := getBootstrap(os.Args)

	// Switch to non-root user and drop root privileges
	if IsRootUser() && lsOpts.User != "" {
		uid := 993
		gid := 990
		AddUser(lsOpts.User, uid, gid)
		if err := os.Chown("/tmp", uid, gid); err != nil {
			log.Warnln("Could not change owner of /tmp:", err)
		}
		UserLogger().Debugln("Process running as root user.")
		DropPrivileges(lsOpts.User)
		UserLogger().Debugln("Process running as non-root user.")
	}

	logCollector := NewLogCollector()

	// file watcher for hot-reloading
	fileWatcherContext, cancelFileWatcher := context.WithCancel(context.Background())

	// build sandbox
	sandbox := rapidcore.
		NewSandboxBuilder().
		//SetTracer(tracer).
		AddShutdownFunc(func() {
			log.Debugln("Stopping file watcher")
			cancelFileWatcher()
			log.Debugln("Stopping DNS server")
			stopDnsServer()
		}).
		SetExtensionsFlag(true).
		SetInitCachingFlag(true)

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
	go RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext)

	// start runtime init
	InitHandler(sandbox.LambdaInvokeAPI(), GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION"), int64(invokeTimeoutSeconds), bootstrap) // TODO: replace this with a custom init

	log.Infoln("Await Initialized ...")
	if err := interopServer.delegate.AwaitInitialized(); err != nil {
		log.Errorln("TODO: send execution environment error status to LocalStack")
		// TODO: distinguish between ErrInitResetReceived and ErrInitDoneFailed
		if err := interopServer.localStackAdapter.SendStatus(Error, []byte{}); err != nil {
			log.Fatalln("TODO: handle LocalStack not reachable and abort")
		}
		return
	}
	log.Infoln("Initialized done. Sending status ready to LocalStack")

	log.Infoln("Send ready status to LocalStack")
	if err := interopServer.localStackAdapter.SendStatus(Ready, []byte{}); err != nil {
		log.Fatalln("TODO: handle LocalStack not reachable and abort")
	}

	<-exitChan
}
