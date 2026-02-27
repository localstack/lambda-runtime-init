package main

import (
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	mlogging "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/logging"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/rapidcore/env"
	log "github.com/sirupsen/logrus"
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
		InteropPort:     GetenvWithDefault("LOCALSTACK_INTEROP_PORT", "9563"),
		InitTracingPort: GetenvWithDefault("LOCALSTACK_RUNTIME_TRACING_PORT", "9564"),
		User:            GetenvWithDefault("LOCALSTACK_USER", "sbx_user1051"),
		InitLogLevel:    GetenvWithDefault("LOCALSTACK_INIT_LOG_LEVEL", "warn"),
		EdgePort:        GetenvWithDefault("EDGE_PORT", "4566"),
		MaxPayloadSize:  GetenvWithDefault("LOCALSTACK_MAX_PAYLOAD_SIZE", "6291556"),
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
	configureLogging(lsOpts.InitLogLevel)

	// Download code archives
	if err := DownloadCodeArchives(lsOpts.CodeArchives); err != nil {
		log.Fatal("Failed to download code archives: " + err.Error())
	}

	if err := AdaptFilesystemPermissions(lsOpts.ChmodPaths); err != nil {
		log.Warnln("Could not change file mode of code directories:", err)
	}

	// Check if running in managed mode
	if _, ok := os.LookupEnv(env.AWS_LAMBDA_MAX_CONCURRENCY); ok {
		runManaged(lsOpts)
		return
	}

	runStandard(lsOpts)
}

func doInitDaemon(addr, port string, enable bool, lvl string) *Daemon {
	endpoint := "http://" + addr + ":" + port
	xrayConfig := initConfig(endpoint, getXRayLogLevel(lvl))
	d := initDaemon(xrayConfig, enable)
	runDaemon(d)
	return d
}

func configureManagedLogger(logLevel string) {
	level := slogLevelFromString(logLevel)
	slog.SetDefault(mlogging.CreateNewLogger(level, io.Writer(os.Stderr)))
}

func configureStandardLogger(logLevel string) {
	log.SetOutput(os.Stderr)
}

func configureLogging(logLevel string) {
	log.SetReportCaller(true)
	switch logLevel {
	case "trace":
		log.SetFormatter(&log.JSONFormatter{})
		log.SetLevel(log.TraceLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	case "fatal":
		log.SetLevel(log.FatalLevel)
	case "panic":
		log.SetLevel(log.PanicLevel)
	default:
		log.Fatal("Invalid value for LOCALSTACK_INIT_LOG_LEVEL")
	}
}

func slogLevelFromString(logLevel string) slog.Level {
	switch logLevel {
	case "trace", "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func getXRayLogLevel(initLogLevel string) string {
	switch initLogLevel {
	case "trace", "debug":
		return "debug"
	case "warn":
		return "warn"
	case "error", "fatal", "panic":
		return "error"
	default:
		return "info"
	}
}
