package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core"
	"go.amzn.com/lambda/core/statejson"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/standalone"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CustomInteropServer struct {
	delegate          *rapidcore.Server
	localStackAdapter *LocalStackAdapter
	port              string
	upstreamEndpoint  string
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
	status_url := fmt.Sprintf("%s/status/%s/%s", l.UpstreamEndpoint, l.RuntimeId, status)
	_, err := http.Post(status_url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return nil
}

// The InvokeRequest is sent by LocalStack to trigger an invocation
type InvokeRequest struct {
	InvokeId           string `json:"invoke-id"`
	InvokedFunctionArn string `json:"invoked-function-arn"`
	Payload            string `json:"payload"`
	TraceId            string `json:"trace-id"`
}

// The ErrorResponse is sent TO LocalStack when encountering an error
type ErrorResponse struct {
	ErrorMessage string   `json:"errorMessage"`
	ErrorType    string   `json:"errorType,omitempty"`
	RequestId    string   `json:"requestId,omitempty"`
	StackTrace   []string `json:"stackTrace,omitempty"`
}

func NewCustomInteropServer(lsOpts *LsOpts, delegate rapidcore.InteropServer, logCollector *LogCollector) (server *CustomInteropServer) {
	server = &CustomInteropServer{
		delegate:         delegate.(*rapidcore.Server),
		port:             lsOpts.InteropPort,
		upstreamEndpoint: lsOpts.RuntimeEndpoint,
		localStackAdapter: &LocalStackAdapter{
			UpstreamEndpoint: lsOpts.RuntimeEndpoint,
			RuntimeId:        lsOpts.RuntimeId,
		},
	}

	// TODO: extract this
	go func() {
		r := chi.NewRouter()
		r.Post("/invoke", func(w http.ResponseWriter, r *http.Request) {
			invokeR := InvokeRequest{}
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

				invokeStart := time.Now()
				err = server.Invoke(invokeResp, &interop.Invoke{
					ID:                 invokeR.InvokeId,
					InvokedFunctionArn: invokeR.InvokedFunctionArn,
					Payload:            strings.NewReader(invokeR.Payload), // r.Body,
					NeedDebugLogs:      true,

					TraceID: invokeR.TraceId,
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
				if err != nil {
					switch err {
					case rapidcore.ErrInvokeTimeout:
						log.Debugf("Got invoke timeout")
						isErr = true
						errorResponse := ErrorResponse{
							ErrorMessage: fmt.Sprintf(
								"%s %s Task timed out after %d.00 seconds",
								time.Now().Format("2006-01-02T15:04:05Z"),
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
				PrintEndReports(invokeR.InvokeId, "", memorySize, invokeStart, timeoutDuration, logCollector)

				serializedLogs, err2 := json.Marshal(logCollector.getLogs())
				if err2 == nil {
					_, err2 = http.Post(server.upstreamEndpoint+"/invocations/"+invokeR.InvokeId+"/logs", "application/json", bytes.NewReader(serializedLogs))
					// TODO: handle err
				}

				var errR map[string]any
				marshalErr := json.Unmarshal(invokeResp.Body, &errR)

				if !isErr && marshalErr == nil {
					_, isErr = errR["errorType"]
				}

				if isErr {
					log.Infoln("Sending to /error")
					_, err = http.Post(server.upstreamEndpoint+"/invocations/"+invokeR.InvokeId+"/error", "application/json", bytes.NewReader(invokeResp.Body))
					if err != nil {
						log.Error(err)
					}
				} else {
					log.Infoln("Sending to /response")
					_, err = http.Post(server.upstreamEndpoint+"/invocations/"+invokeR.InvokeId+"/response", "application/json", bytes.NewReader(invokeResp.Body))
					if err != nil {
						log.Error(err)
					}
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

func (c *CustomInteropServer) StartAcceptingDirectInvokes() error {
	log.Traceln("Function called")
	err := c.localStackAdapter.SendStatus(Ready, []byte{})
	if err != nil {
		return err
	}
	return c.delegate.StartAcceptingDirectInvokes()
}

func (c *CustomInteropServer) SendResponse(invokeID string, contentType string, response io.Reader) error {
	log.Traceln("Function called")
	return c.delegate.SendResponse(invokeID, contentType, response)
}

func (c *CustomInteropServer) SendErrorResponse(invokeID string, response *interop.ErrorResponse) error {
	is, err := c.InternalState()
	if err != nil {
		return err
	}
	rs := is.Runtime.State
	if rs.Name == core.RuntimeInitErrorStateName {
		err = c.localStackAdapter.SendStatus(Error, response.Payload)
		if err != nil {
			return err
		}
	}

	return c.delegate.SendErrorResponse(invokeID, response)
}

func (c *CustomInteropServer) GetCurrentInvokeID() string {
	log.Traceln("Function called")
	return c.delegate.GetCurrentInvokeID()
}

func (c *CustomInteropServer) CommitResponse() error {
	log.Traceln("Function called")
	return c.delegate.CommitResponse()
}

func (c *CustomInteropServer) SendRunning(running *interop.Running) error {
	log.Traceln("Function called")
	return c.delegate.SendRunning(running)
}

func (c *CustomInteropServer) SendRuntimeReady() error {
	log.Traceln("Function called")
	return c.delegate.SendRuntimeReady()
}

func (c *CustomInteropServer) SendDone(done *interop.Done) error {
	log.Traceln("Function called")
	return c.delegate.SendDone(done)
}

func (c *CustomInteropServer) SendDoneFail(fail *interop.DoneFail) error {
	log.Traceln("Function called")
	return c.delegate.SendDoneFail(fail)
}

func (c *CustomInteropServer) StartChan() <-chan *interop.Start {
	log.Traceln("Function called")
	return c.delegate.StartChan()
}

func (c *CustomInteropServer) InvokeChan() <-chan *interop.Invoke {
	log.Traceln("Function called")
	return c.delegate.InvokeChan()
}

func (c *CustomInteropServer) ResetChan() <-chan *interop.Reset {
	log.Traceln("Function called")
	return c.delegate.ResetChan()
}

func (c *CustomInteropServer) ShutdownChan() <-chan *interop.Shutdown {
	log.Traceln("Function called")
	return c.delegate.ShutdownChan()
}

func (c *CustomInteropServer) TransportErrorChan() <-chan error {
	log.Traceln("Function called")
	return c.delegate.TransportErrorChan()
}

func (c *CustomInteropServer) Clear() {
	log.Traceln("Function called")
	c.delegate.Clear()
}

func (c *CustomInteropServer) IsResponseSent() bool {
	log.Traceln("Function called")
	return c.delegate.IsResponseSent()
}

func (c *CustomInteropServer) SetInternalStateGetter(cb interop.InternalStateGetter) {
	log.Traceln("Function called")
	c.delegate.SetInternalStateGetter(cb)
}

func (c *CustomInteropServer) Init(i *interop.Start, invokeTimeoutMs int64) {
	log.Traceln("Function called")
	c.delegate.Init(i, invokeTimeoutMs)
}

func (c *CustomInteropServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	log.Traceln("Function called")
	return c.delegate.Invoke(responseWriter, invoke)
}

func (c *CustomInteropServer) FastInvoke(w http.ResponseWriter, i *interop.Invoke, direct bool) error {
	log.Traceln("Function called")
	return c.delegate.FastInvoke(w, i, direct)
}

func (c *CustomInteropServer) Reserve(id string, traceID, lambdaSegmentID string) (*rapidcore.ReserveResponse, error) {
	log.Traceln("Function called")
	return c.delegate.Reserve(id, traceID, lambdaSegmentID)
}

func (c *CustomInteropServer) Reset(reason string, timeoutMs int64) (*statejson.ResetDescription, error) {
	log.Traceln("Function called")
	return c.delegate.Reset(reason, timeoutMs)
}

func (c *CustomInteropServer) AwaitRelease() (*statejson.InternalStateDescription, error) {
	log.Traceln("Function called")
	return c.delegate.AwaitRelease()
}

func (c *CustomInteropServer) Shutdown(shutdown *interop.Shutdown) *statejson.InternalStateDescription {
	log.Traceln("Function called")
	return c.delegate.Shutdown(shutdown)
}

func (c *CustomInteropServer) InternalState() (*statejson.InternalStateDescription, error) {
	log.Traceln("Function called")
	return c.delegate.InternalState()
}

func (c *CustomInteropServer) CurrentToken() *interop.Token {
	log.Traceln("Function called")
	return c.delegate.CurrentToken()
}
