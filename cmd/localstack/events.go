package main

import (
	"fmt"
	"sync"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/fatalerror"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	lambdatelemetry "github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/telemetry"
)

// LocalStackEventsAPI rides rapidcore's lifecycle events (see doRuntimeDomainInit and doInvoke
// in internal/lambda/rapid/handlers.go) to render the synthetic AWS log lines at the
// AWS-faithful points and to record the outcome of the Init phase:
//
//   - START is emitted on SendInvokeStart, which rapid fires after any inline (suppressed)
//     init and before the runtime handles the invocation — so a re-run init's logs land
//     before START, matching AWS.
//   - INIT_REPORT is emitted on SendInitReport for failed or timed-out inits, with rapid's
//     authoritative duration and phase (init for the eager cold-start init, invoke for a
//     suppressed init folded into an invocation). AWS logs no INIT_REPORT for successful
//     inits; a successful on-demand cold-start init instead surfaces as the first
//     invocation's REPORT "Init Duration" (see TakeColdStartInitDuration).
//   - The scrubbed fatal error type of the most recent failed init (e.g. Runtime.ExitError,
//     Runtime.Unknown) is recorded for the invoke handler's REPORT Status/Error Type line and
//     for the init-failure report to LocalStack (see InitErrorType).
//
// rapid emits, per init attempt: SendInitStart, then SendInitRuntimeDone (status + scrubbed
// error type), then SendInitReport (duration) — RuntimeDone is registered later in
// doRuntimeDomainInit and so runs first on the deferred LIFO unwind. All three fire even when
// the init is aborted by a reset or dies before the runtime starts.
type LocalStackEventsAPI struct {
	// NoOpEventsAPI satisfies the rest of interop.EventsAPI without retaining anything.
	// Do not embed StandaloneEventsAPI here: it appends every platform event to an
	// in-memory event log that is only drained via FetchTailLogs, which this deployment
	// never calls — i.e. unbounded memory growth in warm environments.
	lambdatelemetry.NoOpEventsAPI
	logCollector *LogCollector
	// onDemand mirrors CustomInteropServer.onDemand: only on-demand functions report the
	// cold-start init duration in their first invocation's REPORT line (AWS omits it for
	// provisioned-concurrency and Managed Instances invokes).
	onDemand bool

	mu sync.Mutex
	// lastInitStatus/lastInitErrorType hold the SendInitRuntimeDone outcome of the init
	// attempt currently being reported, reset on SendInitStart. An empty status at
	// SendInitReport time means the init died before the runtime was started (e.g. an
	// extension or bootstrap failure): rapid registers the RuntimeDone callback only after
	// starting the runtime process, so treat empty as an error.
	lastInitStatus    string
	lastInitErrorType string
	// initErrorType is the scrubbed fatal error type of the most recent failed init attempt,
	// reset on SendInitStart. No cross-attempt stickiness is needed: every invocation into a
	// failed-init environment starts a fresh suppressed Init phase (rapidcore shuts the runtime
	// down after an init failure so the next FastInvoke re-inits — see Server.Invoke in
	// rapidcore/server.go), so each failing invocation re-records the failure via its own
	// SendInitReport, and a successful re-run leaves it empty — a recovered environment is not
	// tainted by the original failure.
	initErrorType string
	// initTimedOut is set by main.go before it resets a timed-out init phase, so the aborted
	// init's INIT_REPORT renders as AWS's "Status: timeout" (without an error type) instead of
	// the generic reset error. Consumed by that init's SendInitReport.
	initTimedOut bool
	// coldStartInitDuration buffers rapid's measured duration of a successful on-demand
	// cold-start Init phase until the first invocation's REPORT line consumes it
	// (take-once via TakeColdStartInitDuration).
	coldStartInitDuration    float64
	hasColdStartInitDuration bool
}

func NewLocalStackEventsAPI(logCollector *LogCollector, onDemand bool) *LocalStackEventsAPI {
	return &LocalStackEventsAPI{
		logCollector: logCollector,
		onDemand:     onDemand,
	}
}

func (e *LocalStackEventsAPI) SendInitStart(data interop.InitStartData) error {
	e.mu.Lock()
	e.lastInitStatus, e.lastInitErrorType, e.initErrorType = "", "", ""
	e.mu.Unlock()
	return nil
}

func (e *LocalStackEventsAPI) SendInitRuntimeDone(data interop.InitRuntimeDoneData) error {
	e.mu.Lock()
	e.lastInitStatus = data.Status
	e.lastInitErrorType = ""
	if data.ErrorType != nil {
		e.lastInitErrorType = *data.ErrorType
	}
	e.mu.Unlock()
	return nil
}

func (e *LocalStackEventsAPI) SendInitReport(data interop.InitReportData) error {
	e.mu.Lock()
	status, errorType := e.lastInitStatus, e.lastInitErrorType
	if status == "" {
		// Init died before the runtime was started (see field doc); no scrubbed type known.
		status = lambdatelemetry.RuntimeDoneError
	}
	line := ""
	switch {
	case e.initTimedOut && data.Phase == lambdatelemetry.InitInsideInitPhase:
		// The RIE timed out this init phase and is about to reset it (suppressed-init retry
		// at the first invocation); AWS reports it as a timeout, not as the reset error.
		e.initTimedOut = false
		line = fmt.Sprintf("INIT_REPORT Init Duration: %.2f ms\tPhase: %s\tStatus: timeout\n",
			data.Metrics.DurationMs, data.Phase)
	case status != lambdatelemetry.RuntimeDoneSuccess:
		if errorType == "" {
			// Init died before the runtime was started: rapid recorded no scrubbed type.
			errorType = string(fatalerror.RuntimeExit)
		}
		e.initErrorType = errorType
		line = fmt.Sprintf("INIT_REPORT Init Duration: %.2f ms\tPhase: %s\tStatus: %s\tError Type: %s\n",
			data.Metrics.DurationMs, data.Phase, status, errorType)
	case data.Phase == lambdatelemetry.InitInsideInitPhase && e.onDemand:
		// Successful on-demand cold-start init: reported as the first invocation's
		// REPORT "Init Duration" instead of an INIT_REPORT line.
		e.coldStartInitDuration = data.Metrics.DurationMs
		e.hasColdStartInitDuration = true
	}
	e.mu.Unlock()
	if line != "" {
		_, _ = e.logCollector.Write([]byte(line))
	}
	return nil
}

func (e *LocalStackEventsAPI) SendInvokeStart(data interop.InvokeStartData) error {
	_, _ = fmt.Fprintf(e.logCollector, "START RequestId: %s Version: %s\n", data.RequestID, data.Version)
	return nil
}

// SetInitPhaseTimedOut marks the in-flight init phase as timed out by the RIE, so the
// INIT_REPORT rendered when the aborted init unwinds reports "Status: timeout". Must be
// called before the reset that aborts the init. It also discards a cold-start init duration
// recorded by an init that completed concurrently with the timeout decision.
func (e *LocalStackEventsAPI) SetInitPhaseTimedOut() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initTimedOut = true
	e.hasColdStartInitDuration = false
}

// TakeColdStartInitDuration returns rapid's measured duration of the successful on-demand
// cold-start Init phase, at most once (the duration belongs to the first invocation's REPORT
// only). ok is false on warm starts, after failed or timed-out inits, and for
// non-on-demand (provisioned-concurrency / Managed Instances) environments.
func (e *LocalStackEventsAPI) TakeColdStartInitDuration() (durationMS float64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	durationMS, ok = e.coldStartInitDuration, e.hasColdStartInitDuration
	e.hasColdStartInitDuration = false
	return durationMS, ok
}

// InitErrorType returns the scrubbed fatal error type (e.g. Runtime.ExitError) of the most
// recent failed init attempt, or "" if the latest init attempt succeeded or none was recorded.
func (e *LocalStackEventsAPI) InitErrorType() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.initErrorType
}
