package events

import (
	"encoding/json"
	"fmt"

	"github.com/localstack/lambda-runtime-init/internal/server"

	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/telemetry"
)

type EventsListener struct {
	interop.EventsAPI
	adapter *server.LocalStackAdapter
}

func NewEventsListener(adapter *server.LocalStackAdapter) *EventsListener {
	return &EventsListener{
		adapter: adapter,
		// For now just use the no-ops API to satisfy the EventsAPI interface
		EventsAPI: &telemetry.NoOpEventsAPI{},
	}

}

func (ev *EventsListener) SendFault(data interop.FaultData) error {
	resp := server.ErrorResponse{
		ErrorMessage: fmt.Sprintf("RequestId: %s Error: %s", data.RequestID, data.ErrorMessage),
		ErrorType:    string(data.ErrorType),
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	return ev.adapter.SendStatus(server.Error, payload)
}
