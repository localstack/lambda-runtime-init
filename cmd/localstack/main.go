// main entrypoint of init
// initial structure based upon /cmd/aws-lambda-rie/main.go
package main

import (
	"context"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/rapidcore"
	"net/http"
	_ "net/http/pprof"
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
	PostInvokeWaitMS     string
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
		InitLogLevel:    GetenvWithDefault("LOCALSTACK_INIT_LOG_LEVEL", "debug"),
		EdgePort:        GetenvWithDefault("EDGE_PORT", "4566"),
		// optional or empty
		CodeArchives:        os.Getenv("LOCALSTACK_CODE_ARCHIVES"),
		HotReloadingPaths:   strings.Split(GetenvWithDefault("LOCALSTACK_HOT_RELOADING_PATHS", ""), ","),
		EnableDnsServer:     os.Getenv("LOCALSTACK_ENABLE_DNS_SERVER"),
		EnableXRayTelemetry: os.Getenv("LOCALSTACK_ENABLE_XRAY_TELEMETRY"),
		LocalstackIP:        os.Getenv("LOCALSTACK_HOSTNAME"),
		PostInvokeWaitMS:     os.Getenv("LOCALSTACK_POST_INVOKE_WAIT_MS"),
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

	// set up logging
	log.SetReportCaller(true)
	switch lsOpts.InitLogLevel {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "trace":
		log.SetFormatter(&log.JSONFormatter{})
		log.SetLevel(log.TraceLevel)
	default:
		log.Fatal("Invalid value for LOCALSTACK_INIT_LOG_LEVEL")
	}

	// enable dns server
	dnsServerContext, stopDnsServer := context.WithCancel(context.Background())
	go RunDNSRewriter(lsOpts, dnsServerContext)

	// download code archive if env variable is set
	if err := DownloadCodeArchives(lsOpts.CodeArchives); err != nil {
		log.Fatal("Failed to download code archives")
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
		NewSandboxBuilder(bootstrap).
		//SetTracer(tracer).
		AddShutdownFunc(func() {
			log.Debugln("Stopping file watcher")
			cancelFileWatcher()
			log.Debugln("Stopping DNS server")
			stopDnsServer()
		}).
		SetExtensionsFlag(true).
		SetInitCachingFlag(true).
		SetTailLogOutput(logCollector)

	// xray daemon
	xrayConfig := initConfig("http://" + lsOpts.LocalstackIP + ":" + lsOpts.EdgePort)
	d := initDaemon(xrayConfig, lsOpts.EnableXRayTelemetry == "1")
	sandbox.AddShutdownFunc(func() {
		log.Debugln("Shutting down xray daemon")
		d.stop()
		log.Debugln("Flushing segments in xray daemon")
		d.close()
	})
	runDaemon(d) // async

	defaultInterop := sandbox.InteropServer()
	interopServer := NewCustomInteropServer(lsOpts, defaultInterop, logCollector)
	sandbox.SetInteropServer(interopServer)
	if len(handler) > 0 {
		sandbox.SetHandler(handler)
	}

	// initialize all flows and start runtime API
	go sandbox.Create()

	// get timeout
	invokeTimeoutEnv := GetEnvOrDie("AWS_LAMBDA_FUNCTION_TIMEOUT") // TODO: collect all AWS_* env parsing
	invokeTimeoutSeconds, err := strconv.Atoi(invokeTimeoutEnv)
	if err != nil {
		log.Fatalln(err)
	}
	go RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext)

	// start runtime init
	go InitHandler(sandbox, GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION"), int64(invokeTimeoutSeconds)) // TODO: replace this with a custom init

	// TODO: make the tracing server optional
	// start blocking with the tracing server
	err = http.ListenAndServe("0.0.0.0:"+lsOpts.InitTracingPort, http.DefaultServeMux)
	if err != nil {
		log.Fatal("Failed to start debug server")
	}
}
