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
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/core/statejson"
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
	// initStart is set once in Init() and warmStart is flipped on the first invoke.
	// Both are accessed only from the single sequential init -> invoke flow (the RIE
	// processes one invocation at a time), so they need no additional synchronization.
	initStart time.Time
	warmStart bool
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

func (l *LocalStackAdapter) SendStatus(status LocalStackStatus, payload []byte) error {
	statusUrl := fmt.Sprintf("%s/status/%s/%s", l.UpstreamEndpoint, l.RuntimeId, status)
	resp, err := http.Post(statusUrl, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// SendLogs posts the captured invocation logs to LocalStack.
func (l *LocalStackAdapter) SendLogs(invokeId string, logs lsapi.LogResponse) error {
	serialized, err := json.Marshal(logs)
	if err != nil {
		return err
	}
	resp, err := http.Post(l.UpstreamEndpoint+"/invocations/"+invokeId+"/logs", "application/json", bytes.NewReader(serialized))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
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
	resp, err := http.Post(l.UpstreamEndpoint+endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}


func NewCustomInteropServer(lsOpts *LsOpts, adapter *LocalStackAdapter, delegate interop.Server, logCollector *LogCollector) (server *CustomInteropServer) {
	server = &CustomInteropServer{
		delegate:          delegate.(*rapidcore.Server),
		port:              lsOpts.InteropPort,
		upstreamEndpoint:  lsOpts.RuntimeEndpoint,
		localStackAdapter: adapter,
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
				functionVersion := GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION") // default $LATEST
				_, _ = fmt.Fprintf(logCollector, "START RequestId: %s Version: %s\n", invokeR.InvokeId, functionVersion)

				initDuration := ""
				if !server.warmStart && !invokeR.IsInitRetry {
					initTimeMS := float64(time.Since(server.initStart).Nanoseconds()) / float64(time.Millisecond)
					initDuration = fmt.Sprintf("Init Duration: %.2f ms\t", initTimeMS)
				}
				server.warmStart = true

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

// SendInitErrorResponse forwards the init error to LocalStack and then propagates it to the delegate.
func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
	log.Traceln("SendInitErrorResponse called")

	// Deserialize the raw payload so we can include the requestId and structured fields.
	var parsed struct {
		ErrorMessage string   `json:"errorMessage"`
		ErrorType    string   `json:"errorType"`
		StackTrace   []string `json:"stackTrace,omitempty"`
	}
	if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
		log.WithError(err).Warn("Failed to parse init error payload; forwarding raw payload")
		if err := c.localStackAdapter.SendStatus(Error, resp.Payload); err != nil {
			log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
				Error("Failed to send init error to LocalStack")
		}
		return c.delegate.SendInitErrorResponse(resp)
	}

	requestId := c.delegate.GetCurrentInvokeID()
	adaptedResp := lsapi.ErrorResponse{
		ErrorMessage: parsed.ErrorMessage,
		ErrorType:    parsed.ErrorType,
		RequestId:    &requestId,
		StackTrace:   parsed.StackTrace,
	}
	body, err := json.Marshal(adaptedResp)
	if err != nil {
		log.WithError(err).Error("Failed to marshal adapted init error response")
		body = resp.Payload
	}

	go func() {
		if err := c.localStackAdapter.SendStatus(Error, body); err != nil {
			log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).
				Error("Failed to send init error to LocalStack")
		}
	}()

	return c.delegate.SendInitErrorResponse(resp)
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
