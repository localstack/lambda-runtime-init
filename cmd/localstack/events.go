package main

import (
	"fmt"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore/standalone/telemetry"
)

// LocalStackEventsAPI rides rapidcore's invoke lifecycle events to emit the synthetic START
// log line at the AWS-faithful point. rapidcore calls SendInvokeStart after any inline
// (suppressed) init and before the runtime handles the invocation (see doInvoke in
// internal/lambda/rapid/handlers.go), so emitting START here — rather than eagerly when
// LocalStack dispatches /invoke — places it after a re-run init's logs, matching AWS.
type LocalStackEventsAPI struct {
	*telemetry.StandaloneEventsAPI
	logCollector *LogCollector
}

func NewLocalStackEventsAPI(logCollector *LogCollector) *LocalStackEventsAPI {
	return &LocalStackEventsAPI{
		StandaloneEventsAPI: new(telemetry.StandaloneEventsAPI),
		logCollector:        logCollector,
	}
}

func (e *LocalStackEventsAPI) SendInvokeStart(data interop.InvokeStartData) error {
	_, _ = fmt.Fprintf(e.logCollector, "START RequestId: %s Version: %s\n", data.RequestID, data.Version)
	return e.StandaloneEventsAPI.SendInvokeStart(data)
}
