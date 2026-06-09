package lsapi

// InvokeRequest is sent by LocalStack to trigger an invocation.
type InvokeRequest struct {
	InvokeId           string `json:"invoke-id"`
	InvokedFunctionArn string `json:"invoked-function-arn"`
	Payload            string `json:"payload"`
	TraceId            string `json:"trace-id"`
}

// LogResponse is sent by the runtime to report logs for a completed invocation.
type LogResponse struct {
	Logs string `json:"logs"`
}

// ErrorResponse is sent to LocalStack when encountering an error.
type ErrorResponse struct {
	ErrorMessage string `json:"errorMessage"`
	ErrorType    string `json:"errorType,omitempty"`
	// RequestId uses *string so that an empty string "" is serialized (not omitted),
	// while nil is omitted — init errors always set this field, fault events leave it nil.
	RequestId  *string  `json:"requestId,omitempty"`
	StackTrace []string `json:"stackTrace,omitempty"`
}
