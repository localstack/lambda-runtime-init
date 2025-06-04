package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/localstack/lambda-runtime-init/internal/logging"
	"github.com/localstack/lambda-runtime-init/internal/server"
	log "github.com/sirupsen/logrus"

	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/telemetry"
)

type EventsListener struct {
	interop.EventsAPI

	adapter      *server.LocalStackAdapter
	logCollector *logging.LogCollector

	function server.FunctionConfig

	initStart time.Time
	initEnd   time.Time
}

func NewEventsListener(adapter *server.LocalStackAdapter, eventsAPI interop.EventsAPI, logger *logging.LogCollector, cfg server.FunctionConfig) *EventsListener {
	return &EventsListener{
		adapter:      adapter,
		EventsAPI:    eventsAPI,
		logCollector: logger,
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

func (ev *EventsListener) SendInitStart(_ interop.InitStartData) error {
	ev.initStart = time.Now() // need to use this event to retrieve when the INIT start happens
	return nil
}

func (ev *EventsListener) SendInitRuntimeDone(done interop.InitRuntimeDoneData) error {
	ev.initEnd = time.Now()
	initDurationMs := float64(ev.initEnd.Sub(ev.initStart)) / float64(time.Millisecond) // convert µs -> ms
	switch done.Status {
	case telemetry.RuntimeDoneSuccess:
		return ev.adapter.SendStatus(server.Ready, []byte{})
	case telemetry.RuntimeDoneError:
		if done.ErrorType != nil {
			if done.Phase != telemetry.InitInsideInitPhase {
				log.WithField("ErrorType", *done.ErrorType).Debug("Failed to initialise runtime during INIT phase. This will be retried during the next INVOKE.")
			}
			_, _ = fmt.Fprintf(ev.logCollector, "INIT_REPORT Init Duration: %.2f ms Phase: init Status: %s", initDurationMs, done.Status)
			// return ev.adapter.SendStatus(server.Error, []byte(*done.ErrorType))
		}
	}
	return nil
}

func (ev *EventsListener) SendInvokeStart(invoke interop.InvokeStartData) error {
	_, _ = fmt.Fprintf(ev.logCollector, "START RequestId: %s Version: %s\n", invoke.RequestID, invoke.Version)
	return nil
}

func (ev *EventsListener) SendInvokeRuntimeDone(invoke interop.InvokeRuntimeDoneData) error {
	initDurationMs := float64(ev.initEnd.Sub(ev.initStart)) / float64(time.Millisecond) // convert µs -> ms
	memUsed := invoke.Metrics.ProducedBytes / (1024 * 1024)                             // bytes -> mb

	report := fmt.Sprintf("REPORT RequestId: %s\t"+
		"Duration: %.2f ms\t"+
		"Billed Duration: %.0f ms\t"+
		"Memory Size: %s MB\t"+
		"Max Memory Used: %d MB\t"+
		"Init Duration: %.2f ms",
		invoke.RequestID, invoke.Metrics.DurationMs, ev.function.FunctionMemorySizeMb, memUsed, initDurationMs)

	_, _ = fmt.Fprintln(ev.logCollector, report)

	// we dont send the logs from here since we may want to delay sending logs before we know for certain that
	// the /response has been sent

	return nil
}

func (ev *EventsListener) Reset() {
	ev.initStart = time.Time{}
	ev.initEnd = time.Time{}
}

// func (ev *EventsListener) SendInitReport(data interop.InitReportData) error {

// }
