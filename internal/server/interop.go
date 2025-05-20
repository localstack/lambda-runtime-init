package server

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

	"github.com/go-chi/chi"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	"github.com/localstack/lambda-runtime-init/internal/utils"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/standalone"
)

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

// Create a type-alias to allow the rapidcore.Server to be easier embedded
// into the current implementation.
// TODO: Rename this from delegate.
type delegate = rapidcore.Server

type CustomInteropServer struct {
	// Embed the rapidcore.Server in the custom implementation.
	*delegate

	port             string
	upstreamEndpoint string
	runtimeId        string
}

type LocalStackStatus string

const (
	Ready LocalStackStatus = "ready"
	Error LocalStackStatus = "error"
)

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

func NewCustomInteropServer(lsOpts *LsOpts, delegate interop.Server, logCollector *logging.LogCollector) (server *CustomInteropServer) {
	server = &CustomInteropServer{
		delegate:         delegate.(*rapidcore.Server),
		port:             lsOpts.InteropPort,
		upstreamEndpoint: lsOpts.RuntimeEndpoint,
		runtimeId:        lsOpts.RuntimeId,
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
				functionVersion := utils.GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION") // default $LATEST
				_, _ = fmt.Fprintf(logCollector, "START RequestId: %s Version: %s\n", invokeR.InvokeId, functionVersion)

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
				if err != nil {
					switch {
					case errors.Is(err, rapidcore.ErrInvokeTimeout):
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
				memorySize := utils.GetEnvOrDie("AWS_LAMBDA_FUNCTION_MEMORY_SIZE")
				PrintEndReports(invokeR.InvokeId, "", memorySize, invokeStart, timeoutDuration, logCollector)

				serializedLogs, err2 := json.Marshal(logCollector.GetLogs())
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

func (c *CustomInteropServer) SendStatus(status LocalStackStatus, payload []byte) error {
	statusUrl := fmt.Sprintf("%s/status/%s/%s", c.upstreamEndpoint, c.runtimeId, status)
	_, err := http.Post(statusUrl, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return nil
}

// SendInitErrorResponse writes error response during init to a shared memory and sends GIRD FAULT.
func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
	log.Debug("Forwarding SendInitErrorResponse status to LocalStack at %s.", c.upstreamEndpoint)
	if err := c.SendStatus(Error, resp.Payload); err != nil {
		log.Fatalln("Failed to send init error to LocalStack " + err.Error() + ". Exiting.")
	}
	return c.delegate.SendInitErrorResponse(resp)
}

func (c *CustomInteropServer) SendResponse(invokeID string, resp *interop.StreamableInvokeResponse) error {
	// c.ToggleDirectInvoke()
	return c.delegate.SendResponse(invokeID, resp)
}

func (c *CustomInteropServer) SendErrorResponse(invokeID string, resp *interop.ErrorInvokeResponse) error {
	// c.ToggleDirectInvoke()
	return c.delegate.SendErrorResponse(invokeID, resp)
}

func (c *CustomInteropServer) ToggleDirectInvoke() {
	ctx := c.delegate.GetInvokeContext()
	ctx.Direct = true
}
