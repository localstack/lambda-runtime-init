package server

// Original implementation: lambda/rapidcore/server.go includes Server struct with state
// Server interface between Runtime API and this init: lambda/interop/model.go:Server

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

type FunctionConfig struct {
	FunctionName         string // AWS_LAMBDA_FUNCTION_NAME
	FunctionMemorySizeMb string // AWS_LAMBDA_FUNCTION_MEMORY_SIZE
	FunctionVersion      string // AWS_LAMBDA_FUNCTION_VERSION
	FunctionTimeoutSec   string // AWS_LAMBDA_FUNCTION_TIMEOUT
	InitializationType   string // AWS_LAMBDA_INITIALIZATION_TYPE
	LogGroupName         string // AWS_LAMBDA_LOG_GROUP_NAME
	LogStreamName        string // AWS_LAMBDA_LOG_STREAM_NAME
	FunctionHandler      string //  AWS_LAMBDA_FUNCTION_HANDLER || _HANDLER
}

type LocalStackStatus string

const (
	Ready LocalStackStatus = "ready"
	Error LocalStackStatus = "error"
)

// The InvokeRequest is sent by LocalStack to trigger an invocation
type InvokeRequest struct {
	InvokeId           string `json:"invoke-id"`
	InvokedFunctionArn string `json:"invoked-function-arn"`
	Payload            string `json:"payload"`
	TraceId            string `json:"trace-id"`
}
