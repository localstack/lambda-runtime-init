package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInvokeID = "test-invoke-id-12345"

// newTestRouter creates a chi router with the same LocalStack API routes as main(),
// without the debug /test and /fail endpoints.
func newTestRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Post("/invocations/{invoke_id}/response", invokeResponseHandler)
	r.Post("/invocations/{invoke_id}/error", invokeErrorHandler)
	r.Post("/invocations/{invoke_id}/logs", invokeLogsHandler)
	r.Post("/status/{runtime_id}/{status}", statusHandler)
	return r
}

// TestInvocationResponseReturns202 verifies POST /invocations/{id}/response returns 202 Accepted.
// LocalStack's executor_endpoint.py invocation_response returns HTTPStatus.ACCEPTED.
func TestInvocationResponseReturns202(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/invocations/"+testInvokeID+"/response",
		"application/json",
		bytes.NewBufferString(`{"result":"ok"}`),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// TestInvocationErrorReturns202 verifies POST /invocations/{id}/error returns 202 Accepted.
// LocalStack's executor_endpoint.py invocation_error returns HTTPStatus.ACCEPTED.
func TestInvocationErrorReturns202(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	body := `{"errorMessage":"something went wrong","errorType":"RuntimeError","stackTrace":[]}`
	resp, err := http.Post(
		srv.URL+"/invocations/"+testInvokeID+"/error",
		"application/json",
		bytes.NewBufferString(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// TestInvocationLogsReturns202 verifies POST /invocations/{id}/logs returns 202 Accepted
// and accepts a {"logs":"..."} JSON body as sent by custom_interop.go via LogResponse.
func TestInvocationLogsReturns202(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	logPayload, err := json.Marshal(LogResponse{
		Logs: "START RequestId: " + testInvokeID + " Version: $LATEST\nEND RequestId: " + testInvokeID + "\n",
	})
	require.NoError(t, err)

	resp, err := http.Post(
		srv.URL+"/invocations/"+testInvokeID+"/logs",
		"application/json",
		bytes.NewReader(logPayload),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// TestStatusReadyReturns202AndTriggersInvoke verifies that POST /status/{runtime_id}/ready:
//   - returns 202 Accepted (matching LocalStack executor_endpoint.py status_ready)
//   - asynchronously sends a POST to the invoke endpoint with a valid InvokeRequest body
func TestStatusReadyReturns202AndTriggersInvoke(t *testing.T) {
	invokeCh := make(chan InvokeRequest, 1)
	captureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req InvokeRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		invokeCh <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer captureServer.Close()

	origInvokeUrl := invokeUrl
	invokeUrl = captureServer.URL + "/invoke"
	defer func() { invokeUrl = origInvokeUrl }()

	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/status/runtime-id-123/ready",
		"application/json",
		bytes.NewBufferString(""),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	select {
	case req := <-invokeCh:
		assert.NotEmpty(t, req.InvokeId, "invoke-id must be set in the triggered InvokeRequest")
		assert.NotEmpty(t, req.Payload, "payload must be set in the triggered InvokeRequest")
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for invoke request to be sent after status/ready")
	}
}

// TestStatusErrorReturns202 verifies POST /status/{runtime_id}/error returns 202 Accepted.
// LocalStack's executor_endpoint.py status_error returns HTTPStatus.ACCEPTED on the first call.
func TestStatusErrorReturns202(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/status/runtime-id-123/error",
		"application/json",
		bytes.NewBufferString(`{"errorMessage":"init failed","errorType":"InitError"}`),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// TestInvokeRequestJSONFieldNames verifies that InvokeRequest uses the exact JSON field names
// that LocalStack sends to the runtime's /invoke endpoint (as defined in custom_interop.go).
//
// WARNING: The LocalStack<->RIE API contract is currently unversioned. Any change to these
// field names is a silent breaking change that requires a coordinated update of both
// localstack-pro and lambda-runtime-init with no safe rollback path.
func TestInvokeRequestJSONFieldNames(t *testing.T) {
	raw := `{
		"invoke-id":             "abc-123",
		"invoked-function-arn":  "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
		"payload":               "{\"key\":\"value\"}",
		"trace-id":              "Root=1-abc;Parent=def;Sampled=1"
	}`

	var req InvokeRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	assert.Equal(t, "abc-123", req.InvokeId)
	assert.Equal(t, "arn:aws:lambda:us-east-1:000000000000:function:my-fn", req.InvokedFunctionArn)
	assert.Equal(t, `{"key":"value"}`, req.Payload)
	assert.Equal(t, "Root=1-abc;Parent=def;Sampled=1", req.TraceId)
}

// TestLogResponseJSONFieldName verifies that LogResponse uses the "logs" key
// expected by LocalStack's executor_endpoint.py invocation_logs handler.
func TestLogResponseJSONFieldName(t *testing.T) {
	raw := `{"logs":"START RequestId: abc\nEND RequestId: abc\n"}`

	var lr LogResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &lr))

	assert.Equal(t, "START RequestId: abc\nEND RequestId: abc\n", lr.Logs)
}
