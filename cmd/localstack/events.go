package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore/standalone/telemetry"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lsapi"
)

// LocalStackEventsAPI intercepts fault events and forwards them to LocalStack as error status callbacks.
type LocalStackEventsAPI struct {
	*telemetry.StandaloneEventsAPI
	adapter   *LocalStackAdapter
	requestID string
	mu        sync.RWMutex
}

func NewLocalStackEventsAPI(adapter *LocalStackAdapter) *LocalStackEventsAPI {
	return &LocalStackEventsAPI{
		adapter:             adapter,
		StandaloneEventsAPI: new(telemetry.StandaloneEventsAPI),
	}
}

func (ev *LocalStackEventsAPI) SendFault(data interop.FaultData) error {
	_ = ev.StandaloneEventsAPI.SendFault(data)

	requestID := string(data.RequestID)
	if data.RequestID == "" {
		ev.mu.RLock()
		requestID = ev.requestID
		ev.mu.RUnlock()
	}

	resp := lsapi.ErrorResponse{
		ErrorMessage: fmt.Sprintf("RequestId: %s Error: %s", requestID, data.ErrorMessage),
		ErrorType:    string(data.ErrorType),
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	return ev.adapter.SendStatus(Error, payload)
}

func (ev *LocalStackEventsAPI) SetCurrentRequestID(id interop.RequestID) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.requestID = string(id)
	ev.StandaloneEventsAPI.SetCurrentRequestID(id)
}
