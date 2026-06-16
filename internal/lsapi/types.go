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
//
// It is used for payloads the platform synthesizes itself (e.g. Sandbox.Timedout,
// Runtime.ExitError). On AWS these carry no requestId/stackTrace fields — unlike
// runtime-reported error payloads, which include a blank "requestId" and possibly an empty
// "stackTrace" (validated against AWS in localstack-pro's test_lambda.py snapshots, e.g.
// test_lambda_invoke_with_timeout and test_lambda_init_timeout_then_crash vs
// test_lambda_handler_not_found). Runtime-reported payloads are therefore forwarded
// verbatim (see adaptInitErrorPayload) rather than re-marshaled through this struct, and
// the omitempty tags here are deliberate.
type ErrorResponse struct {
	ErrorMessage string   `json:"errorMessage"`
	ErrorType    string   `json:"errorType,omitempty"`
	RequestId    string   `json:"requestId,omitempty"`
	StackTrace   []string `json:"stackTrace,omitempty"`
}
