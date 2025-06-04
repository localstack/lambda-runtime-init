package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/go-chi/chi"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/fatalerror"
	"go.amzn.com/lambda/rapidcore"
)

func NewServer(port string, api *LocalStackService) *http.Server {
	r := chi.NewRouter()

	r.Post("/invoke", InvokeHandler(api))

	return &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}
}

func InvokeHandler(api *LocalStackService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Ensure we always return a response to caller
		defer func() {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		}()

		// TODO: We shouldn't be using a custom request body here,
		// instead we should be forwarding the entire boto3/AWS request
		var req InvokeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.WithError(err).Error("Failed to decode invoke request")
		}

		response, err := api.InvokeForward(req)
		switch {
		// case errors.Is(err, rapidcore.ErrInvokeDoneFailed) || err == nil:
		// we can actually just continue here, error message is sent below
		case errors.Is(err, rapidcore.ErrInvokeTimeout):
			log.Debugf("Got invoke timeout")
			errorResponse := ErrorResponse{
				ErrorMessage: fmt.Sprintf(
					"RequestId: %s Error: Task timed out after %s.00 seconds",
					req.InvokeId,
					api.function.FunctionTimeoutSec,
				),
				ErrorType: "Sandbox.Timedout",
			}

			errorResponseJson, err := json.Marshal(errorResponse)
			if err != nil {
				log.Fatalln("unable to marshal json timeout response")
			}
			response = errorResponseJson
			// default:
			// 	log.Fatalln(err)
		}

		api.AfterInvoke(r.Context())
		api.CollectAndSendLogs(req.InvokeId)

		invokeRespDecoder := json.NewDecoder(bytes.NewReader(response))
		invokeRespDecoder.DisallowUnknownFields()

		// If the response cannot be decoded into an ErrorResponse type,
		// we assume no error is present.
		var errorResponse ErrorResponse
		if err := invokeRespDecoder.Decode(&errorResponse); err != nil || reflect.ValueOf(errorResponse).IsZero() {
			api.SendResponse(req.InvokeId, response)
			return
		}

		switch errorResponse.ErrorType {
		case string(fatalerror.FunctionOversizedResponse):
			// Emulator returns incorrect response
			errorResponse.ErrorMessage = fmt.Sprintf("Response payload size exceeded maximum allowed payload size (%s bytes).", api.runtime.MaxPayloadSize)
			if modifiedBody, err := json.Marshal(errorResponse); err == nil {
				response = modifiedBody
			}
		}
		api.SendError(req.InvokeId, response)
	}

}
