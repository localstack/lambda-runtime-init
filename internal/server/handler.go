package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/fatalerror"
	"go.amzn.com/lambda/rapi/model"
	"go.amzn.com/lambda/rapidcore/standalone"
)

func NewServer(port string, api *LocalStackAPI) *http.Server {
	r := chi.NewRouter()

	r.Post("/invoke", InvokeHandler(api))

	return &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}
}

func InvokeHandler(api *LocalStackAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: We shouldn't be using a custom request body here,
		// instead we should be forwarding the entire boto3/AWS request
		var req InvokeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.WithError(err).Error("Failed to decode invoke request")
		}

		proxyReq, err := http.NewRequest(r.Method, r.URL.String(), strings.NewReader(req.Payload))
		if err != nil {
			log.WithError(err).Error("Failed to create proxy request")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Ensure we always return a response to caller
		defer func() {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		}()

		// TODO: Add client context to request or as a header ("X-Amz-Client-Context")
		// Copy relevant headers from original request
		proxyReq.Header.Set("X-Amzn-Trace-Id", req.TraceId)
		proxyReq.Header.Set("X-Amzn-Request-Id", req.InvokeId)

		proxyResponseWriter := &standalone.ResponseWriterProxy{}

		// Use standalone's Execute which handles all the error cases
		standalone.Execute(proxyResponseWriter, proxyReq, api)

		if err := api.AfterInvoke(); err != nil {
			log.WithError(err).Error("Post-invocation step failed.")
		}

		api.CollectAndSendLogs(req.InvokeId)

		var errorResponse model.ErrorResponse
		isValidError := json.Unmarshal(proxyResponseWriter.Body, &errorResponse) == nil

		if !proxyResponseWriter.IsError() || !isValidError {
			api.SendResponse(req.InvokeId, proxyResponseWriter.Body)
			return
		}

		switch errorResponse.ErrorType {
		case string(fatalerror.FunctionOversizedResponse):
			// Emulator returns incorrect response
			errorResponse.ErrorMessage = "Response payload size exceeded maximum allowed payload size (6291556 bytes)."
			if modifiedBody, err := json.Marshal(errorResponse); err == nil {
				proxyResponseWriter.Body = modifiedBody
			}
		}
		api.SendError(req.InvokeId, proxyResponseWriter.Body)
	}

}
