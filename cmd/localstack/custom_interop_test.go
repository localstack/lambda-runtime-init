package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lsapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- JSON contract tests ---

// TestInvokeRequestContract verifies that InvokeRequest correctly maps the JSON field names
// that LocalStack sends to the RIE's /invoke endpoint (defined in
// localstack-pro/localstack-core/localstack/services/lambda_/invocation/execution_environment.py).
//
// WARNING: The LocalStack↔RIE API contract is currently unversioned. Any change to these
// field names is a silent breaking change that requires a coordinated update of both
// localstack-pro and lambda-runtime-init with no safe rollback path.
func TestInvokeRequestContract(t *testing.T) {
	raw := `{
		"invoke-id":            "abc-123",
		"invoked-function-arn": "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
		"payload":              "{\"key\":\"value\"}",
		"trace-id":             "Root=1-abc;Parent=def;Sampled=1"
	}`

	var req lsapi.InvokeRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	assert.Equal(t, "abc-123", req.InvokeId)
	assert.Equal(t, "arn:aws:lambda:us-east-1:000000000000:function:my-fn", req.InvokedFunctionArn)
	assert.Equal(t, `{"key":"value"}`, req.Payload)
	assert.Equal(t, "Root=1-abc;Parent=def;Sampled=1", req.TraceId)
}

// TestLogResponseContract verifies that LogResponse uses the "logs" JSON key expected by
// LocalStack's invocation_logs handler (executor_endpoint.py).
func TestLogResponseContract(t *testing.T) {
	raw := `{"logs":"START RequestId: abc\nEND RequestId: abc\n"}`

	var lr lsapi.LogResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &lr))

	assert.Equal(t, "START RequestId: abc\nEND RequestId: abc\n", lr.Logs)
}

// --- LocalStackAdapter.SendStatus tests ---

func TestSendStatus_ReadySendsToCorrectPath(t *testing.T) {
	var capturedReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL, RuntimeId: "runtime-abc"}
	require.NoError(t, adapter.SendStatus(Ready, []byte{}))

	assert.Equal(t, http.MethodPost, capturedReq.Method)
	assert.Equal(t, "/status/runtime-abc/ready", capturedReq.URL.Path)
}

func TestSendStatus_ErrorSendsToCorrectPath(t *testing.T) {
	var capturedReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL, RuntimeId: "runtime-abc"}
	require.NoError(t, adapter.SendStatus(Error, []byte(`{"errorMessage":"init failed"}`)))

	assert.Equal(t, http.MethodPost, capturedReq.Method)
	assert.Equal(t, "/status/runtime-abc/error", capturedReq.URL.Path)
}

// --- LocalStackAdapter.SendLogs tests ---

func TestSendLogs_SendsJSONWithLogsKey(t *testing.T) {
	var capturedPath string
	var capturedBody lsapi.LogResponse
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL}
	logs := lsapi.LogResponse{Logs: "START RequestId: invoke-1\nEND RequestId: invoke-1\n"}
	require.NoError(t, adapter.SendLogs("invoke-1", logs))

	assert.Equal(t, "/invocations/invoke-1/logs", capturedPath)
	assert.Equal(t, logs.Logs, capturedBody.Logs)
}

// --- LocalStackAdapter.SendResult routing tests ---

func TestSendResult_SuccessGoesToResponseEndpoint(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL}
	require.NoError(t, adapter.SendResult("invoke-1", []byte(`{"result":"ok"}`), false))

	assert.Equal(t, "/invocations/invoke-1/response", capturedPath)
}

func TestSendResult_ErrorBodyGoesToErrorEndpoint(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	// Body contains "errorType" — LocalStack distinguishes function errors this way
	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL}
	errBody := []byte(`{"errorMessage":"something went wrong","errorType":"RuntimeError"}`)
	require.NoError(t, adapter.SendResult("invoke-1", errBody, false))

	assert.Equal(t, "/invocations/invoke-1/error", capturedPath)
}

func TestSendResult_ExplicitErrorFlagGoesToErrorEndpoint(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	// isError=true covers cases like timeout where the RIE itself constructs the error body
	adapter := &LocalStackAdapter{UpstreamEndpoint: srv.URL}
	require.NoError(t, adapter.SendResult("invoke-1", []byte(`{"errorMessage":"Task timed out"}`), true))

	assert.Equal(t, "/invocations/invoke-1/error", capturedPath)
}
