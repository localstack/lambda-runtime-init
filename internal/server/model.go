package server

import (
	"bufio"
	"bytes"
	"net/http"
)

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

type ResponseWriterProxy struct {
	writer *bufio.Writer
	buffer *bytes.Buffer

	StatusCode int
	header     http.Header
}

func NewResponseWriterProxy() *ResponseWriterProxy {
	buffer := bytes.NewBuffer(nil)
	return &ResponseWriterProxy{
		writer: bufio.NewWriter(buffer),
		buffer: buffer,
	}
}

func (w *ResponseWriterProxy) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}

func (w *ResponseWriterProxy) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *ResponseWriterProxy) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
}

func (w *ResponseWriterProxy) Flush() {
	_ = w.writer.Flush()
}

func (w *ResponseWriterProxy) IsError() bool {
	return w.StatusCode != 0 && w.StatusCode/100 != 2
}

func (w *ResponseWriterProxy) Body() []byte {
	w.Flush()
	return w.buffer.Bytes()
}
