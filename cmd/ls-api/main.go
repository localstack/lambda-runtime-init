// Simple testing utility to emulate the internal LocalStack Endpoint
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	log "github.com/sirupsen/logrus"
	"io"
	"net/http"
)

const apiPort = 9563
const listenPort = 48490

var invokeUrl = fmt.Sprintf("http://localhost:%d/invoke", apiPort)

func main() {
	// mock for Localstack component

	uid := "12345"

	router := chi.NewRouter()
	router.Use(middleware.Logger)
	router.Post("/invocations/{invoke_id}/response", invokeResponseHandler)
	router.Post("/invocations/{invoke_id}/error", invokeErrorHandler)
	router.Post("/invocations/{invoke_id}/logs", invokeLogsHandler)
	router.Post("/status/{runtime_id}/{status}", statusHandler)

	router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		invokeRequest, _ := json.Marshal(InvokeRequest{InvokeId: uid, Payload: "{\"counter\":0}"})
		_, err := http.Post(invokeUrl, "application/json", bytes.NewReader(invokeRequest))
		if err != nil {
			log.Error(err)
		}

		w.WriteHeader(200)
		_, err = w.Write([]byte("hi"))
		if err != nil {
			log.Error(err)
		}
	})

	router.Get("/fail", func(w http.ResponseWriter, r *http.Request) {
		invokeRequest, _ := json.Marshal(InvokeRequest{InvokeId: uid, Payload: "{\"counter\":0, \"fail\": \"yes\"}"})
		_, err := http.Post(invokeUrl, "application/json", bytes.NewReader(invokeRequest))
		if err != nil {
			log.Error(err)
		}

		w.WriteHeader(200)
		_, err = w.Write([]byte("hi"))
		if err != nil {
			log.Error(err)
		}
	})

    log.Infof("Listening on port :%d", listenPort)
	err := http.ListenAndServe(fmt.Sprintf(":%d", listenPort), router)
	if err != nil {
		log.Fatal(err)
	}
}

func invokeLogsHandler(w http.ResponseWriter, r *http.Request) {
	invokeId := chi.URLParam(r, "invoke_id")
	log.Println(invokeId)
	var logResponse LogResponse
	if err := json.NewDecoder(r.Body).Decode(&logResponse); err != nil {
		log.Error("invalid logs payload: ", err)
	} else {
		log.Println("log result: " + logResponse.Logs)
	}
	w.WriteHeader(http.StatusAccepted)
}

// InvokeRequest is sent by LocalStack to trigger an invocation.
type InvokeRequest struct {
	InvokeId           string `json:"invoke-id"`
	InvokedFunctionArn string `json:"invoked-function-arn"`
	Payload            string `json:"payload"`
	TraceId            string `json:"trace-id"`
}

// LogResponse is sent by the runtime to report logs for a completed invocation.
type LogResponse struct {
	Logs string `json:"logs"`
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	runtime_id := chi.URLParam(r, "runtime_id")
	status := chi.URLParam(r, "status")
	log.Println(runtime_id + " + " + status)
	if status == "ready" {
		go func() {
			invokeRequest, _ := json.Marshal(InvokeRequest{InvokeId: "12345", Payload: "{\"counter\":0}"})
			_, err := http.Post(invokeUrl, "application/json", bytes.NewReader(invokeRequest))
			if err != nil {
				log.Error(err)
			}
		}()
	}
	w.WriteHeader(http.StatusAccepted)
}

func invokeResponseHandler(w http.ResponseWriter, r *http.Request) {
	invokeId := chi.URLParam(r, "invoke_id")
	log.Println(invokeId)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error(err)
	}
	log.Println("result: " + string(bodyBytes))
	w.WriteHeader(http.StatusAccepted)
}

func invokeErrorHandler(w http.ResponseWriter, r *http.Request) {
	invokeId := chi.URLParam(r, "invoke_id")
	log.Println(invokeId)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error(err)
	}
	log.Println("error result: " + string(bodyBytes))
	w.WriteHeader(http.StatusAccepted)
}
