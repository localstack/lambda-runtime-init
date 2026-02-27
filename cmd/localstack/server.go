package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	rie "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/aws-lambda-rie"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/invoke"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/logging"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/rapid"
	rapidmodel "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/rapid/model"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda-managed-instances/raptor"

	log "github.com/sirupsen/logrus"
)

type adaptedInvokeRequest struct {
	interop.InvokeRequest

	inReq          InvokeRequest
	maxPayloadSize int64
}

func NewAdaptedInvokeRequest(r *http.Request, w http.ResponseWriter, invokeReq InvokeRequest, maxPayloadSize int64) *adaptedInvokeRequest {
	// HACK(gregfurman): We use the original request to construct a new interop.InvokeRequest via struct embedding. We then compose
	// a new adaptedInvokeRequest type (that implements interop.InvokeRequest iface) that is adapted to suit our LocalStack internals.

	dummyReq := r.Clone(context.Background())
	dummyReq.Body = io.NopCloser(strings.NewReader(invokeReq.Payload))

	internalReq := rie.NewRieInvokeRequest(dummyReq, w)

	return &adaptedInvokeRequest{
		InvokeRequest:  internalReq,
		inReq:          invokeReq,
		maxPayloadSize: int64(maxPayloadSize),
	}
}

func (r *adaptedInvokeRequest) MaxPayloadSize() int64 {
	return r.maxPayloadSize
}

func (r *adaptedInvokeRequest) InvokeId() string {
	return r.inReq.InvokeId
}

func (r *adaptedInvokeRequest) TraceId() string {
	return r.inReq.TraceId
}

//--------------------------------------------------------------------------------------------

type InvokeHandler struct {
	app         *raptor.App
	initRequest model.InitRequestMessage
	doInit      func() rapidmodel.AppError

	upstreamEndpoint string
	logCollector     *LogCollector

	maxPayloadSize int64
}

func NewInvokeHandler(lsOpts LsOpts, init model.InitRequestMessage, app *raptor.App, logCollector *LogCollector) (*InvokeHandler, error) {
	payloadSize, err := strconv.Atoi(lsOpts.MaxPayloadSize)
	if err != nil {
		log.Panicln("Please specify a number for LOCALSTACK_MAX_PAYLOAD_SIZE")
	}

	h := &InvokeHandler{
		initRequest:      init,
		app:              app,
		maxPayloadSize:   int64(payloadSize),
		upstreamEndpoint: lsOpts.RuntimeEndpoint,
		logCollector:     logCollector,
	}

	h.doInit = sync.OnceValue(func() rapidmodel.AppError {
		initCtx, cancel := context.WithTimeout(context.Background(), time.Duration(h.initRequest.InitTimeout))
		defer cancel()

		dummyInitMetrics := rapid.NewInitMetrics(nil)
		res := h.app.Init(initCtx, &h.initRequest, dummyInitMetrics)
		return res
	})

	return h, nil
}

func (h *InvokeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/invoke" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var invokeReq InvokeRequest
	if err := json.Unmarshal(bodyBytes, &invokeReq); err != nil {
		http.Error(w, "Invalid invoke request", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))

	go h.invoke(r, invokeReq)
}

func (h *InvokeHandler) Init() rapidmodel.AppError {
	return h.doInit()
}

func (h *InvokeHandler) invoke(r *http.Request, invokeReq InvokeRequest) {
	recorder := httptest.NewRecorder()

	defer func() {
		go h.sendUpstreamCallbacks(invokeReq.InvokeId, recorder.Body.Bytes(), recorder.Code)
	}()

	if err := h.doInit(); err != nil {
		log.Errorf("init failed: %v", err)
		h.respondWithError(recorder, err)
		return
	}

	adaptedInvokeReq := NewAdaptedInvokeRequest(r, recorder, invokeReq, int64(h.maxPayloadSize))

	ctx := logging.WithInvokeID(context.Background(), adaptedInvokeReq.InvokeID())
	metrics := invoke.NewInvokeMetrics(nil, &noOpCounter{})
	metrics.AttachInvokeRequest(adaptedInvokeReq)

	appErr, responseSent := h.app.Invoke(ctx, adaptedInvokeReq, metrics)
	if appErr != nil {
		log.Errorf("invoke failed: %v", appErr)
		if !responseSent {
			h.respondWithError(recorder, appErr)
		}
	}
}

func (h *InvokeHandler) respondWithError(w http.ResponseWriter, err rapidmodel.AppError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.ReturnCode())
	w.Write([]byte(err.ErrorDetails()))
}

func (h *InvokeHandler) sendUpstreamCallbacks(invokeID string, responseBody []byte, statusCode int) {
	if invokeID == "" {
		return
	}

	if h.logCollector != nil {
		if logs := h.logCollector.getLogs(); logs.Logs != "" {
			serializedLogs, _ := json.Marshal(logs)
			_, err := http.Post(
				h.upstreamEndpoint+"/invocations/"+invokeID+"/logs",
				"application/json",
				bytes.NewReader(serializedLogs),
			)
			if err != nil {
				log.Errorf("Failed to send logs upstream: %v", err)
			}
		}
	}

	var errResp map[string]any
	isError := false
	if json.Unmarshal(responseBody, &errResp) == nil {
		_, hasErrorType := errResp["errorType"]
		_, hasErrorMessage := errResp["errorMessage"]
		isError = hasErrorType || hasErrorMessage
	}

	if statusCode >= 400 {
		isError = true
	}

	endpoint := "/response"
	if isError {
		endpoint = "/error"
	}

	_, err := http.Post(
		h.upstreamEndpoint+"/invocations/"+invokeID+endpoint,
		"application/json",
		bytes.NewReader(responseBody),
	)
	if err != nil {
		log.Errorf("Failed to send %s upstream: %v", endpoint, err)
	}
}

type noOpCounter struct{}

func (c *noOpCounter) AddInvoke(_ uint64) {}
