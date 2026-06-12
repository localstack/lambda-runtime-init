package main

// Original implementation: lambda/rapidcore/server.go includes Server struct with state
// Server interface between Runtime API and this init: lambda/interop/model.go:Server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/core/statejson"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/fatalerror"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore/standalone"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lsapi"
	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"
)

type CustomInteropServer struct {
	delegate          *rapidcore.Server
	localStackAdapter *LocalStackAdapter
	port              string
	upstreamEndpoint  string
	// eventsAPI renders the synthetic START/INIT_REPORT log lines from rapid's lifecycle
	// events and records the init outcome (error type, cold-start duration) — see events.go.
	eventsAPI *LocalStackEventsAPI
	// initErrorPayload stashes the structured error payload the runtime reported via
	// /init/error ([]byte), so ReportInitFailure can forward the runtime's own error to
	// LocalStack instead of a synthesized one. Written from the runtime API handler flow and
	// read from the main flow after init failed, hence atomic.
	initErrorPayload atomic.Value
}

type LocalStackAdapter struct {
	UpstreamEndpoint string
	RuntimeId        string
}

type LocalStackStatus string

const (
	Ready LocalStackStatus = "ready"
	Error LocalStackStatus = "error"
)

// post sends a JSON payload to the given LocalStack endpoint path and fails on non-2xx
// responses (e.g. LocalStack rejects a duplicate /status/error with 400).
func (l *LocalStackAdapter) post(path string, payload []byte) error {
	resp, err := http.Post(l.UpstreamEndpoint+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s returned status %d", path, resp.StatusCode)
	}
	return nil
}

func (l *LocalStackAdapter) SendStatus(status LocalStackStatus, payload []byte) error {
	return l.post(fmt.Sprintf("/status/%s/%s", l.RuntimeId, status), payload)
}

// SendLogs posts the captured invocation logs to LocalStack.
func (l *LocalStackAdapter) SendLogs(invokeId string, logs lsapi.LogResponse) error {
	serialized, err := json.Marshal(logs)
	if err != nil {
		return err
	}
	return l.post("/invocations/"+invokeId+"/logs", serialized)
}

// SendResult posts the invocation result body to LocalStack.
// If isError is false, the body is also inspected for an "errorType" field — its
// presence indicates a Lambda function error and routes the result to /error.
func (l *LocalStackAdapter) SendResult(invokeId string, body []byte, isError bool) error {
	if !isError {
		var fields map[string]any
		if json.Unmarshal(body, &fields) == nil {
			_, isError = fields["errorType"]
		}
	}
	endpoint := "/invocations/" + invokeId + "/response"
	if isError {
		log.Infoln("Sending to /error")
		endpoint = "/invocations/" + invokeId + "/error"
	} else {
		log.Infoln("Sending to /response")
	}
	return l.post(endpoint, body)
}

func NewCustomInteropServer(lsOpts *LsOpts, delegate interop.Server, logCollector *LogCollector, eventsAPI *LocalStackEventsAPI) (server *CustomInteropServer) {
	server = &CustomInteropServer{
		delegate:         delegate.(*rapidcore.Server),
		port:             lsOpts.InteropPort,
		upstreamEndpoint: lsOpts.RuntimeEndpoint,
		localStackAdapter: &LocalStackAdapter{
			UpstreamEndpoint: lsOpts.RuntimeEndpoint,
			RuntimeId:        lsOpts.RuntimeId,
		},
		eventsAPI: eventsAPI,
	}

	// TODO: extract this
	go func() {
		r := chi.NewRouter()
		r.Post("/invoke", func(w http.ResponseWriter, r *http.Request) {
			invokeR := lsapi.InvokeRequest{}
			bytess, err := io.ReadAll(r.Body)
			if err != nil {
				log.Error(err)
			}

			go func() {
				err = json.Unmarshal(bytess, &invokeR)
				if err != nil {
					log.Error(err)
				}

				invokeResp := &standalone.ResponseWriterProxy{}
				// The synthetic START and INIT_REPORT lines are emitted via LocalStackEventsAPI
				// from rapid's lifecycle events, so they land at the AWS-faithful points (e.g.
				// after an inline suppressed init's own logs) — see events.go.

				// First invocation into a successfully initialized on-demand environment: REPORT
				// carries the Init phase duration as measured by rapid (take-once; empty on warm
				// starts, failed/timed-out inits, and non-on-demand environments).
				initDuration := ""
				if initTimeMS, ok := server.eventsAPI.TakeColdStartInitDuration(); ok {
					initDuration = fmt.Sprintf("Init Duration: %.2f ms\t", initTimeMS)
				}

				invokeStart := time.Now()
				err = server.Invoke(invokeResp, &interop.Invoke{
					ID:                 invokeR.InvokeId,
					InvokedFunctionArn: invokeR.InvokedFunctionArn,
					Payload:            strings.NewReader(invokeR.Payload), // r.Body,
					NeedDebugLogs:      true,
					TraceID:            invokeR.TraceId,
					// TODO: set correct segment ID from request
					//LambdaSegmentID:    "LambdaSegmentID", // r.Header.Get("X-Amzn-Segment-Id"),
					//CognitoIdentityID:     "",
					//CognitoIdentityPoolID: "",
					//DeadlineNs:            "",
					//ClientContext:         "",
					//ContentType:           "",
					//ReservationToken:      "",
					//VersionID:             "",
					//InvokeReceivedTime:    0,
					//ResyncState:           interop.Resync{},
				})
				timeout := int(server.delegate.GetInvokeTimeout().Seconds())
				isErr := false
				status := ""
				if err != nil {
					switch {
					case errors.Is(err, rapidcore.ErrInvokeTimeout):
						log.Debugf("Got invoke timeout")
						isErr = true
						status = "Status: timeout"
						errorResponse := lsapi.ErrorResponse{
							ErrorType: "Sandbox.Timedout",
							ErrorMessage: fmt.Sprintf(
								"RequestId: %s Error: Task timed out after %d.00 seconds",
								invokeR.InvokeId,
								timeout,
							),
						}
						jsonErrorResponse, err := json.Marshal(errorResponse)
						if err != nil {
							log.Fatalln("unable to marshall json timeout response")
						}
						_, err = invokeResp.Write(jsonErrorResponse)
						if err != nil {
							log.Fatalln("unable to write to response")
						}
					case errors.Is(err, rapidcore.ErrInvokeDoneFailed):
						// The error response body was already written by rapid and is sent below.
						// When an init failure was folded into this invocation (AWS suppressed
						// init), the REPORT additionally carries the failure status and the
						// scrubbed fatal error type (e.g. Runtime.Unknown).
						if errType := server.eventsAPI.InitErrorType(); errType != "" {
							isErr = true
							status = "Status: error\tError Type: " + errType
						}
					default:
						log.Fatalln(err)
					}
				}
				// optional sleep. can be used for debugging purposes
				if lsOpts.PostInvokeWaitMS != "" {
					waitMS, err := strconv.Atoi(lsOpts.PostInvokeWaitMS)
					if err != nil {
						log.Fatalln(err)
					}
					time.Sleep(time.Duration(waitMS) * time.Millisecond)
				}
				timeoutDuration := time.Duration(timeout) * time.Second
				memorySize := GetEnvOrDie("AWS_LAMBDA_FUNCTION_MEMORY_SIZE")
				PrintEndReports(invokeR.InvokeId, initDuration, status, memorySize, invokeStart, timeoutDuration, logCollector)

				if err2 := server.localStackAdapter.SendLogs(invokeR.InvokeId, logCollector.getLogs()); err2 != nil {
					log.Error("failed to send logs to LocalStack: ", err2)
				}
				if err2 := server.localStackAdapter.SendResult(invokeR.InvokeId, invokeResp.Body, isErr); err2 != nil {
					log.Error("failed to send result to LocalStack: ", err2)
				}
			}()

			w.WriteHeader(200)
			_, _ = w.Write([]byte("OK"))
		})
		err := http.ListenAndServe(":"+server.port, r)
		if err != nil {
			log.Error(err)
		}

	}()

	return server
}

func (c *CustomInteropServer) SendResponse(invokeID string, resp *interop.StreamableInvokeResponse) error {
	log.Traceln("SendResponse called")
	return c.delegate.SendResponse(invokeID, resp)
}

func (c *CustomInteropServer) SendErrorResponse(invokeID string, resp *interop.ErrorInvokeResponse) error {
	log.Traceln("SendErrorResponse called")
	return c.delegate.SendErrorResponse(invokeID, resp)
}

// SendInitErrorResponse stashes the init error reported by the runtime (via /init/error) for
// ReportInitFailure and propagates it to the delegate, which caches it so the first invoke
// can surface it. The delegate's error is returned because the /runtime/init/error handler
// renders an interop error to the runtime based on it (e.g. ErrResponseSent during a
// suppressed init).
func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
	log.Traceln("SendInitErrorResponse called")
	c.initErrorPayload.Store(resp.Payload)
	return c.delegate.SendInitErrorResponse(resp)
}

// ReportInitFailure reports a failed initialization to LocalStack via /status/error, failing
// the environment's startup. It forwards the runtime's own /init/error payload when one was
// reported, and synthesizes a structured error from the given type and message otherwise
// (e.g. when the runtime crashed, called sys.exit, or had an invalid entrypoint).
// Only main.go calls this, and only for environments that fail provisioning-time (extended
// init: provisioned concurrency / Managed Instances); on-demand environments fold init
// failures into the first invocation instead.
func (c *CustomInteropServer) ReportInitFailure(errType fatalerror.ErrorType, message string) {
	payload, _ := c.initErrorPayload.Load().([]byte)
	if payload == nil {
		// Match AWS's fault message format "RequestId: <id> Error: <msg>". No invocation is
		// active during the init phase (LocalStack only dispatches invokes after the runtime
		// reports ready), so the request id is blank — matching the /init/error path below,
		// which forwards AWS's blank init-phase requestId.
		body, err := json.Marshal(lsapi.ErrorResponse{
			ErrorType:    string(errType),
			ErrorMessage: fmt.Sprintf("RequestId: %s Error: %s", c.delegate.GetCurrentInvokeID(), message),
		})
		if err != nil {
			log.WithError(err).Error("Failed to marshal init error response")
			return
		}
		payload = body
	} else if adapted := adaptInitErrorPayload(payload, c.delegate.GetCurrentInvokeID()); adapted != nil {
		payload = adapted
	}

	if err := c.localStackAdapter.SendStatus(Error, payload); err != nil {
		log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
			Error("Failed to send init error to LocalStack")
	}
}

// adaptInitErrorPayload injects the requestId into the runtime's structured /init/error
// payload, preserving all other fields exactly as the runtime emitted them — in particular an
// empty but present "stackTrace": [] (e.g. Runtime.HandlerNotFound), which a typed struct with
// omitempty would drop on re-marshal.
//
// AWS includes a (blank) "requestId" in runtime-reported init error payloads but NOT in
// platform-synthesized ones (e.g. Runtime.ExitError) — see the lsapi.ErrorResponse doc for the
// AWS-snapshot evidence. The two ReportInitFailure paths intentionally differ in this regard.
// Returns nil if the payload cannot be adapted (it is then forwarded unmodified).
func adaptInitErrorPayload(payload []byte, requestID string) []byte {
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		log.WithError(err).Warn("Failed to parse init error payload; forwarding raw payload")
		return nil
	}
	fields["requestId"] = requestID
	adapted, err := json.Marshal(fields)
	if err != nil {
		log.WithError(err).Error("Failed to marshal adapted init error payload")
		return nil
	}
	return adapted
}

func (c *CustomInteropServer) GetCurrentInvokeID() string {
	log.Traceln("GetCurrentInvokeID called")
	return c.delegate.GetCurrentInvokeID()
}

func (c *CustomInteropServer) SendRuntimeReady() error {
	log.Traceln("SendRuntimeReady called")
	return c.delegate.SendRuntimeReady()
}

func (c *CustomInteropServer) Init(i *interop.Init, invokeTimeoutMs int64) error {
	log.Traceln("Init called")
	return c.delegate.Init(i, invokeTimeoutMs)
}

func (c *CustomInteropServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	log.Traceln("Invoke called")
	return c.delegate.Invoke(responseWriter, invoke)
}

func (c *CustomInteropServer) FastInvoke(w http.ResponseWriter, i *interop.Invoke, direct bool) error {
	log.Traceln("FastInvoke called")
	return c.delegate.FastInvoke(w, i, direct)
}

func (c *CustomInteropServer) Reserve(id string, traceID, lambdaSegmentID string) (*rapidcore.ReserveResponse, error) {
	log.Traceln("Reserve called")
	return c.delegate.Reserve(id, traceID, lambdaSegmentID)
}

func (c *CustomInteropServer) Reset(reason string, timeoutMs int64) (*statejson.ResetDescription, error) {
	log.Traceln("Reset called")
	return c.delegate.Reset(reason, timeoutMs)
}

func (c *CustomInteropServer) AwaitRelease() (*statejson.ReleaseResponse, error) {
	log.Traceln("AwaitRelease called")
	return c.delegate.AwaitRelease()
}

func (c *CustomInteropServer) InternalState() (*statejson.InternalStateDescription, error) {
	log.Traceln("InternalState called")
	return c.delegate.InternalState()
}

func (c *CustomInteropServer) CurrentToken() *interop.Token {
	log.Traceln("CurrentToken called")
	return c.delegate.CurrentToken()
}

func (c *CustomInteropServer) SetSandboxContext(sbCtx interop.SandboxContext) {
	log.Traceln("SetSandboxContext called")
	c.delegate.SetSandboxContext(sbCtx)
}

func (c *CustomInteropServer) SetInternalStateGetter(cb interop.InternalStateGetter) {
	log.Traceln("SetInternalStateGetter called")
	c.delegate.InternalStateGetter = cb
}
