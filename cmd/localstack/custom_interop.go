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
	// logCollector accumulates the runtime's stdout/stderr plus the synthetic START/REPORT/
	// INIT_REPORT lines that are flushed to LocalStack with each invocation's logs.
	logCollector *LogCollector
	// eventsAPI provides rapid's authoritative Init-phase duration (see events.go), used for
	// the REPORT/INIT_REPORT log lines instead of wall-clock measurements at invoke arrival.
	eventsAPI *LocalStackEventsAPI
	// initStart is set once in Init() and warmStart is flipped on the first invoke.
	// Both are accessed only from the single sequential init -> invoke flow (the RIE
	// processes one invocation at a time), so they need no additional synchronization.
	initStart time.Time
	warmStart bool
	// initTimedOut is set by ReportInitTimeout when the init phase exceeds its timeout. It is
	// written from the init-await flow and read from the invoke flow, so it uses atomic access.
	// When set, the first invocation's REPORT omits Init Duration (init was already reported as
	// timed out and is re-run as a suppressed init during that invocation).
	initTimedOut atomic.Bool
	// initErrorForwarded is set once the runtime's own /init/error has been forwarded to
	// LocalStack via SendInitErrorResponse, so the crash-path fallback (SendInitError) does
	// not send a duplicate error status for the same failed initialization. Unlike
	// initErrorType below it is never cleared: it only guards the one-shot init-phase report.
	initErrorForwarded atomic.Bool
	// initErrorType holds rapidcore's scrubbed fatal error type (e.g. Runtime.Unknown) when init
	// failed, used to render the INIT_REPORT(phase=invoke) and REPORT Status/Error Type lines for
	// the on-demand folded-into-invoke path. Stores a string; empty/unset means init did not fail.
	// It persists while invocations keep failing (each one re-runs the init as a suppressed init
	// and AWS re-emits the failure envelope), and is cleared by the invoke handler once an
	// invocation succeeds so a recovered environment is not tainted by the original failure.
	initErrorType atomic.Value
	// onDemand is true for on-demand functions, where AWS folds a failed cold-start init into
	// the first invocation (suppressed init). For these we do NOT report init failures via
	// /status/error; instead we signal ready and let the first invoke surface the error with
	// the full INIT_REPORT/START/END/REPORT envelope. Provisioned concurrency and Managed
	// Instances keep the provisioning-time /status/error model. SnapStart environments are
	// also classified on-demand here (LocalStack sets AWS_LAMBDA_INITIALIZATION_TYPE=on-demand
	// for them and initializes them lazily at the first invoke, not at version publish), so the
	// fold-into-invoke model applies to them too.
	// TODO: set AWS_LAMBDA_INITIALIZATION_TYPE=snap-start on the LocalStack side for env-var
	// parity with AWS once SnapStart environments get their own initialization type.
	onDemand bool
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

func NewCustomInteropServer(lsOpts *LsOpts, adapter *LocalStackAdapter, delegate interop.Server, logCollector *LogCollector, eventsAPI *LocalStackEventsAPI) (server *CustomInteropServer) {
	server = &CustomInteropServer{
		delegate:          delegate.(*rapidcore.Server),
		port:              lsOpts.InteropPort,
		upstreamEndpoint:  lsOpts.RuntimeEndpoint,
		localStackAdapter: adapter,
		logCollector:      logCollector,
		eventsAPI:         eventsAPI,
		onDemand:          GetenvWithDefault("AWS_LAMBDA_INITIALIZATION_TYPE", "on-demand") == "on-demand",
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
				// The synthetic START line is emitted via LocalStackEventsAPI.SendInvokeStart so it
				// lands after any inline (suppressed) init, matching AWS — see events.go.

				initErrType, _ := server.initErrorType.Load().(string)

				// First invocation into a successfully initialized on-demand environment: REPORT
				// carries the Init phase duration as measured by rapid (init start -> init end).
				// Provisioned concurrency / Managed Instances initialize at provisioning time and
				// AWS omits Init Duration from their invokes' REPORT lines.
				initDuration := ""
				if server.onDemand && !server.warmStart && !server.initTimedOut.Load() && initErrType == "" {
					if initTimeMS, ok := server.eventsAPI.InitDurationMS(); ok {
						initDuration = fmt.Sprintf("Init Duration: %.2f ms\t", initTimeMS)
					}
				}
				server.warmStart = true

				// On-demand init failure folded into this invocation (AWS suppressed init): emit
				// the INIT_REPORT(phase=invoke) line before START (emitted during Invoke below),
				// reporting the failed init's duration (rapid's measurement when available; the
				// wall-clock fallback covers inits that died before emitting INIT_REPORT).
				if initErrType != "" {
					initTimeMS, ok := server.eventsAPI.InitDurationMS()
					if !ok {
						initTimeMS = millisSince(server.initStart)
					}
					fprintInitReport(logCollector, initTimeMS, "invoke", "error", initErrType)
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
						// we can actually just continue here, error message is sent below
					default:
						log.Fatalln(err)
					}
				}
				// On-demand init failure folded into this invocation: when the suppressed init
				// re-run (and thus the invoke) failed again, the REPORT carries the failure status
				// and rapidcore's scrubbed fatal error type (e.g. Runtime.Unknown). When the
				// invocation succeeded (the suppressed re-init recovered from a transient init
				// failure), the result stands on its own — AWS reports it as successful — and the
				// cached init failure is cleared so later invocations are not tainted by it.
				if initErrType != "" {
					if err != nil {
						isErr = true
						status = "Status: error\tError Type: " + initErrType
					} else {
						server.initErrorType.Store("")
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

// SendInitErrorResponse forwards the init error reported by the runtime (via /init/error) to
// LocalStack and then propagates it to the delegate. It marks initErrorForwarded so the
// crash-path fallback in main.go (SendInitError) does not send a duplicate error status for
// the same failed initialization.
func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) (err error) {
	log.Traceln("SendInitErrorResponse called")
	// Mark synchronously, before sending: this runs in the init flow before
	// AwaitInitializedWithDetails unblocks in main.go, so the fallback observes the flag.
	c.initErrorForwarded.Store(true)
	// Record rapidcore's scrubbed fatal error type so the folded-into-invoke path can render the
	// INIT_REPORT(phase=invoke) and REPORT Status/Error Type lines (on-demand).
	c.initErrorType.Store(string(resp.FunctionError.Type))

	// Always cache the structured error in the delegate so the first invoke can surface it, and
	// return its error: the /runtime/init/error handler renders an interop error to the runtime
	// based on it (e.g. ErrResponseSent during a suppressed init).
	defer func() { err = c.delegate.SendInitErrorResponse(resp) }()

	// On-demand folds the failed init into the first invocation, which carries the error and
	// logs; reporting it here via /status/error too would race the invoke and fail the env
	// startup before the invoke runs. PC/SnapStart/MI report at provisioning time below.
	if c.onDemand {
		return nil
	}

	// Forward the runtime's structured payload as-is and only inject the requestId. Decoding
	// into a map rather than a typed struct preserves fields exactly as the runtime emitted
	// them — in particular an empty but present "stackTrace": [] (e.g. Runtime.HandlerNotFound),
	// which a typed struct with omitempty would drop on re-marshal.
	var payload map[string]any
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		log.WithError(err).Warn("Failed to parse init error payload; forwarding raw payload")
		if err := c.localStackAdapter.SendStatus(Error, resp.Payload); err != nil {
			log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
				Error("Failed to send init error to LocalStack")
		}
		return nil
	}

	// No invocation is active during the init phase, so this is typically blank; AWS still
	// includes a (blank) requestId in the init error payload.
	payload["requestId"] = c.delegate.GetCurrentInvokeID()

	body, err := json.Marshal(payload)
	if err != nil {
		log.WithError(err).Error("Failed to marshal adapted init error response")
		body = resp.Payload
	}

	if err := c.localStackAdapter.SendStatus(Error, body); err != nil {
		log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
			Error("Failed to send init error to LocalStack")
	}
	return nil
}

// SendInitError reports a structured init failure to LocalStack when the runtime failed to
// initialize WITHOUT calling /init/error itself (e.g. it crashed, called sys.exit, or had an
// invalid entrypoint). The init failure is detected by the existing rapidcore machinery
// (watchEvents -> InitFailure -> AwaitInitializedWithDetails) and surfaced to main.go.
// It is a no-op if SendInitErrorResponse already forwarded the runtime's own structured error.
func (c *CustomInteropServer) SendInitError(errType fatalerror.ErrorType, errMsg error) {
	if c.initErrorForwarded.Load() {
		log.Debug("Init error already forwarded to LocalStack; skipping duplicate")
		return
	}

	if errType == "" {
		errType = fatalerror.RuntimeExit
	}

	message := "Runtime exited during initialization"
	if errMsg != nil {
		message = errMsg.Error()
	}

	// Match AWS's fault message format "RequestId: <id> Error: <msg>". No invocation is active
	// during the init phase (LocalStack only dispatches an invoke after the runtime reports
	// ready), so the request id is blank — matching the /init/error path, which forwards AWS's
	// blank init-phase requestId (see SendInitErrorResponse).
	payload, err := json.Marshal(lsapi.ErrorResponse{
		ErrorType:    string(errType),
		ErrorMessage: fmt.Sprintf("RequestId: %s Error: %s", c.delegate.GetCurrentInvokeID(), message),
	})
	if err != nil {
		log.WithError(err).Error("Failed to marshal init error response")
		return
	}

	if err := c.localStackAdapter.SendStatus(Error, payload); err != nil {
		log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
			Error("Failed to send init error to LocalStack")
	}
}

// RecordInitError records the structured init failure detected by rapidcore for runtimes that
// failed WITHOUT calling /init/error (crash, sys.exit, invalid entrypoint), so the on-demand
// folded-into-invoke path renders the same INIT_REPORT(phase=invoke) and REPORT Status/Error
// Type lines as the /init/error-reported flavor. It must not overwrite a type already recorded
// by SendInitErrorResponse: the runtime-reported error is the authoritative one.
func (c *CustomInteropServer) RecordInitError(errType fatalerror.ErrorType) {
	if recorded, _ := c.initErrorType.Load().(string); recorded != "" {
		return
	}
	if errType == "" {
		errType = fatalerror.RuntimeExit
	}
	c.initErrorType.Store(string(errType))
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
	c.initStart = time.Now()
	return c.delegate.Init(i, invokeTimeoutMs)
}

// ReportInitTimeout emits an AWS-style INIT_REPORT timeout line into the log collector and
// marks the init as timed out. The init is then re-run as a suppressed init during the first
// invocation (under the function timeout), and that invocation's REPORT omits Init Duration.
func (c *CustomInteropServer) ReportInitTimeout() {
	c.initTimedOut.Store(true)
	fprintInitReport(c.logCollector, millisSince(c.initStart), "init", "timeout", "")
}

// ReportInitPhaseError emits the AWS-style INIT_REPORT(phase=init, status=error) line for an
// on-demand cold-start init that failed (e.g. a runtime crash or exit during module load).
// AWS performs a suppressed double init: the failed cold-start init reports Phase: init here,
// and the retried init folded into the first invocation reports Phase: invoke (see the invoke
// handler). It is a no-op when no init error was recorded. The duration is rapid's measurement
// of the Init phase when available, falling back to wall-clock for inits that died before
// emitting their INIT_REPORT lifecycle event.
func (c *CustomInteropServer) ReportInitPhaseError() {
	errType, _ := c.initErrorType.Load().(string)
	if errType == "" {
		return
	}
	initTimeMS, ok := c.eventsAPI.InitDurationMS()
	if !ok {
		initTimeMS = millisSince(c.initStart)
	}
	fprintInitReport(c.logCollector, initTimeMS, "init", "error", errType)
}

// millisSince returns the wall-clock milliseconds elapsed since start.
func millisSince(start time.Time) float64 {
	return float64(time.Since(start).Nanoseconds()) / float64(time.Millisecond)
}

// fprintInitReport emits an AWS-style INIT_REPORT log line, e.g.
// "INIT_REPORT Init Duration: 9999.27 ms\tPhase: init\tStatus: timeout" or
// "INIT_REPORT Init Duration: 0.91 ms\tPhase: invoke\tStatus: error\tError Type: Runtime.ExitError".
func fprintInitReport(w io.Writer, durationMS float64, phase string, status string, errorType string) {
	_, _ = fmt.Fprintf(w, "INIT_REPORT Init Duration: %.2f ms\tPhase: %s\tStatus: %s", durationMS, phase, status)
	if errorType != "" {
		_, _ = fmt.Fprintf(w, "\tError Type: %s", errorType)
	}
	_, _ = fmt.Fprintln(w)
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
