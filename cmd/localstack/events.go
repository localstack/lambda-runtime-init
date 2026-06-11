package main

import (
	"fmt"
	"sync/atomic"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/rapidcore/standalone/telemetry"
	lambdatelemetry "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/telemetry"
)

// LocalStackEventsAPI rides rapidcore's invoke lifecycle events to emit the synthetic START
// log line at the AWS-faithful point. rapidcore calls SendInvokeStart after any inline
// (suppressed) init and before the runtime handles the invocation (see doInvoke in
// internal/lambda/rapid/handlers.go), so emitting START here — rather than eagerly when
// LocalStack dispatches /invoke — places it after a re-run init's logs, matching AWS.
type LocalStackEventsAPI struct {
	*telemetry.StandaloneEventsAPI
	logCollector *LogCollector
	// initDurationMS holds rapid's authoritative measurement of the Init phase (init start ->
	// init end, monotonic), captured from the INIT_REPORT(phase=init) lifecycle event emitted
	// by doRuntimeDomainInit. The /invoke handler renders it as the first invocation's REPORT
	// "Init Duration" instead of re-measuring at invoke arrival, which would wrongly include
	// the idle gap between init completion and the first invoke dispatch.
	initDurationMS atomic.Value // float64
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

func (e *LocalStackEventsAPI) SendInitReport(data interop.InitReportData) error {
	if data.Phase == lambdatelemetry.InitInsideInitPhase {
		e.initDurationMS.Store(data.Metrics.DurationMs)
	}
	return e.StandaloneEventsAPI.SendInitReport(data)
}

// InitDurationMS returns rapid's measured duration of the startup Init phase and whether one
// was recorded (false e.g. when the init phase timed out and never completed).
func (e *LocalStackEventsAPI) InitDurationMS() (float64, bool) {
	durationMS, ok := e.initDurationMS.Load().(float64)
	return durationMS, ok
}
