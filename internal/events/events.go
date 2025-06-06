package events

import (
	"encoding/json"
	"fmt"

	"github.com/localstack/lambda-runtime-init/internal/localstack"

	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore/standalone/telemetry"
)

// LocalStackEventsAPI handles internally emitted rapid events.
// TODO: Logs should all be collected here
type LocalStackEventsAPI struct {
	interop.EventsAPI
	adapter *localstack.LocalStackClient
}

func NewLocalStackEventsAPI(adapter *localstack.LocalStackClient) *LocalStackEventsAPI {
	return &LocalStackEventsAPI{
		adapter:   adapter,
		EventsAPI: new(telemetry.StandaloneEventsAPI),
	}
}

func (ev *LocalStackEventsAPI) SendFault(data interop.FaultData) error {
	// We can ignore whatever errors are returned here
	_ = ev.EventsAPI.SendFault(data)

	resp := localstack.ErrorResponse{
		ErrorMessage: fmt.Sprintf("RequestId: %s Error: %s", data.RequestID, data.ErrorMessage),
		ErrorType:    string(data.ErrorType),
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	return ev.adapter.SendStatus(localstack.Error, payload)
}
