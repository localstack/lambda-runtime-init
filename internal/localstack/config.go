package localstack

// Original implementation: lambda/rapidcore/server.go includes Server struct with state
// Server interface between Runtime API and this init: lambda/interop/model.go:Server

type Config struct {
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

// The InvokeRequest is sent by LocalStack to trigger an invocation
type InvokeRequest struct {
	InvokeId           string `json:"request-id"`
	InvokedFunctionArn string `json:"invoked-function-arn"`
	Payload            string `json:"payload"`
	TraceId            string `json:"trace-id"`
	ClientContext      string `json:"client-context"`
}

// The ErrorResponse is sent TO LocalStack when encountering an error
type ErrorResponse struct {
	ErrorMessage string   `json:"errorMessage"`
	ErrorType    string   `json:"errorType,omitempty"`
	RequestId    *string  `json:"requestId,omitempty"`
	StackTrace   []string `json:"stackTrace,omitempty"`
	Trace        []string `json:"trace,omitempty"`
}
