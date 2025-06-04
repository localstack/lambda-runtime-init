package events

import (
	"encoding/json"
	"fmt"

	"github.com/localstack/lambda-runtime-init/internal/server"
	"go.amzn.com/lambda/interop"
)

type EventsListener struct {
	interop.EventsAPI

	adapter *server.LocalStackAdapter
}

func NewEventsListener(adapter *server.LocalStackAdapter, eventsAPI interop.EventsAPI) *EventsListener {
	return &EventsListener{
		adapter:   adapter,
		EventsAPI: eventsAPI,
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
