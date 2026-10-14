package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
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
	assert.Empty(t, req.ClientContext)
}

// TestInvokeRequestClientContextContract checks that JSON encoding preserves the base64
// context verbatim and omits an empty context. Decoding belongs to the invocation handler.
func TestInvokeRequestClientContextContract(t *testing.T) {
	raw := `{"client-context":"eyJjdXN0b20iOnsiZm9vIjoiYmFyIn19"}`
	var req lsapi.InvokeRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	assert.Equal(t, "eyJjdXN0b20iOnsiZm9vIjoiYmFyIn19", req.ClientContext)
	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"client-context":"eyJjdXN0b20iOnsiZm9vIjoiYmFyIn19"`)
	encoded, err = json.Marshal(lsapi.InvokeRequest{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "client-context")
}

// TestInvokeHandlerForwardsContextPerRequest reuses one handler across successive requests
// to check decoding, context isolation, and preservation of the remaining invocation fields.
func TestInvokeHandlerForwardsContextPerRequest(t *testing.T) {
	dispatched := make(chan *interop.Invoke, 1)
	handler := newInvokeHandler(func(invoke *interop.Invoke) { dispatched <- invoke })
	contexts := []struct {
		name    string
		context string
		present bool
	}{
		{"full", `{"custom":{"foo":"bar"},"client":{"app_title":"test"},"env":{"platform":"test"}}`, true},
		{"nested", `{"custom":{"nested":[1,true,null],"unicode":"\u00e9"}}`, true},
		// Neither form of an absent context may inherit the preceding request's value.
		{"omitted", "", false},
		{"empty", "", true},
		// Base64-looking data inside the decoded JSON must remain untouched.
		{"decoded-once", `"eyJmb28iOiJiYXIifQ=="`, true},
	}
	for _, test := range contexts {
		t.Run(test.name, func(t *testing.T) {
			wire := map[string]string{
				"invoke-id":            test.name,
				"invoked-function-arn": "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
				"payload":              `{"value":"hello"}`,
				"trace-id":             "Root=1-abc;Parent=def;Sampled=1",
			}
			if test.present {
				wire["client-context"] = base64.StdEncoding.EncodeToString([]byte(test.context))
			}
			body, err := json.Marshal(wire)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))))
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, "OK", recorder.Body.String())
			select {
			case invoke := <-dispatched:
				assert.Equal(t, test.context, invoke.ClientContext)
				assert.Equal(t, test.name, invoke.ID)
				assert.Equal(t, wire["invoked-function-arn"], invoke.InvokedFunctionArn)
				assert.Equal(t, wire["trace-id"], invoke.TraceID)
				assert.True(t, invoke.NeedDebugLogs)
				payload, err := io.ReadAll(invoke.Payload)
				require.NoError(t, err)
				assert.Equal(t, wire["payload"], string(payload))
			case <-time.After(time.Second):
				t.Fatal("valid invocation was not dispatched")
			}
		})
	}
}

type failedInvocationBody struct{}

func (failedInvocationBody) Read([]byte) (int, error) {
	return 0, errors.New("body read failed")
}

// TestInvokeHandlerRejectsInvalidRequests checks that bad input gets HTTP 400 and unreadable
// bodies get HTTP 500, with neither case starting a Lambda invocation in the background.
func TestInvokeHandlerRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   io.Reader
		status int
	}{
		{"malformed-json", strings.NewReader(`{invalid`), http.StatusBadRequest},
		{"null", strings.NewReader(`null`), http.StatusBadRequest},
		{"array", strings.NewReader(`[]`), http.StatusBadRequest},
		{"invalid-base64", strings.NewReader(`{"client-context":"not base64!"}`), http.StatusBadRequest},
		{"wrong-context-type", strings.NewReader(`{"client-context":42}`), http.StatusBadRequest},
		{"body-read-failure", failedInvocationBody{}, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatched := make(chan *interop.Invoke, 1)
			handler := newInvokeHandler(func(invoke *interop.Invoke) { dispatched <- invoke })
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/invoke", test.body))
			assert.Equal(t, test.status, recorder.Code)
			select {
			case <-dispatched:
				t.Fatal("invalid invocation was dispatched")
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

// TestInvokeHandlerAcknowledgesBeforeCompletion holds the invocation open while checking
// that /invoke returns 200 OK. Execution results are delivered later through callbacks.
func TestInvokeHandlerAcknowledgesBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	completed := make(chan struct{})
	defer close(release)
	handler := newInvokeHandler(func(*interop.Invoke) { <-release; close(completed) })
	returned := make(chan struct{})
	recorder := httptest.NewRecorder()
	go func() {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(`{"invoke-id":"async"}`)))
		close(returned)
	}()
	select {
	case <-returned:
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "OK", recorder.Body.String())
	case <-time.After(time.Second):
		t.Fatal("handler waited for invocation completion")
	}
	select {
	case <-completed:
		t.Fatal("invocation completed before release")
	default:
	}
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
