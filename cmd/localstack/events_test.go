package main

import (
	"testing"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sendInit(t *testing.T, e *LocalStackEventsAPI, phase interop.InitPhase, status string, errorType string, durationMS float64) {
	t.Helper()
	require.NoError(t, e.SendInitStart(interop.InitStartData{Phase: phase}))
	if status != "" {
		var errType *string
		if errorType != "" {
			errType = &errorType
		}
		require.NoError(t, e.SendInitRuntimeDone(interop.InitRuntimeDoneData{
			Phase:     phase,
			Status:    status,
			ErrorType: errType,
		}))
	}
	require.NoError(t, e.SendInitReport(interop.InitReportData{
		Phase:   phase,
		Metrics: interop.InitReportMetrics{DurationMs: durationMS},
	}))
}

// --- INIT_REPORT rendering ---

func TestEventsAPI_SuccessfulOnDemandInit_NoInitReportLine_DurationTakenOnce(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	sendInit(t, e, "init", "success", "", 123.45)

	// AWS logs no INIT_REPORT for successful inits ...
	assert.Empty(t, logs.getLogs().Logs)
	// ... the duration surfaces as the first invocation's REPORT "Init Duration" instead.
	durationMS, ok := e.TakeColdStartInitDuration()
	assert.True(t, ok)
	assert.Equal(t, 123.45, durationMS)
	// Take-once: warm-start invocations must not report it again.
	_, ok = e.TakeColdStartInitDuration()
	assert.False(t, ok)
	assert.Empty(t, e.InitErrorType())
}

func TestEventsAPI_SuccessfulProvisionedInit_NoDurationRecorded(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, false)

	sendInit(t, e, "init", "success", "", 123.45)

	// AWS omits Init Duration from provisioned-concurrency invokes' REPORT lines.
	_, ok := e.TakeColdStartInitDuration()
	assert.False(t, ok)
}

func TestEventsAPI_FailedInit_RendersInitReportAndRecordsErrorType(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	sendInit(t, e, "init", "error", "Runtime.Unknown", 9.87)

	assert.Equal(t,
		"INIT_REPORT Init Duration: 9.87 ms\tPhase: init\tStatus: error\tError Type: Runtime.Unknown\n",
		logs.getLogs().Logs)
	assert.Equal(t, "Runtime.Unknown", e.InitErrorType())
	// A failed init must not leak an Init Duration into the first invocation's REPORT.
	_, ok := e.TakeColdStartInitDuration()
	assert.False(t, ok)
}

func TestEventsAPI_FailedSuppressedInit_RendersInvokePhase(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	sendInit(t, e, "invoke", "error", "Runtime.ExitError", 0.91)

	assert.Equal(t,
		"INIT_REPORT Init Duration: 0.91 ms\tPhase: invoke\tStatus: error\tError Type: Runtime.ExitError\n",
		logs.getLogs().Logs)
	assert.Equal(t, "Runtime.ExitError", e.InitErrorType())
}

func TestEventsAPI_InitDiedBeforeRuntimeStart_FallsBackToRuntimeExit(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	// No SendInitRuntimeDone: the init died before rapid started the runtime process.
	sendInit(t, e, "init", "", "", 1.23)

	assert.Equal(t,
		"INIT_REPORT Init Duration: 1.23 ms\tPhase: init\tStatus: error\tError Type: Runtime.ExitError\n",
		logs.getLogs().Logs)
	assert.Equal(t, "Runtime.ExitError", e.InitErrorType())
}

func TestEventsAPI_TimedOutInit_RendersTimeoutStatusOnce(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	// main.go marks the timeout, then resets the init; the aborted init unwinds with an
	// error status that must render as AWS's "Status: timeout" without an error type.
	e.SetInitPhaseTimedOut()
	sendInit(t, e, "init", "error", "Runtime.Unknown", 10001.5)

	assert.Equal(t,
		"INIT_REPORT Init Duration: 10001.50 ms\tPhase: init\tStatus: timeout\n",
		logs.getLogs().Logs)
	// The timeout mapping is consumed: a later suppressed init reports its own outcome.
	logs.reset()
	sendInit(t, e, "invoke", "error", "Runtime.ExitError", 2.5)
	assert.Equal(t,
		"INIT_REPORT Init Duration: 2.50 ms\tPhase: invoke\tStatus: error\tError Type: Runtime.ExitError\n",
		logs.getLogs().Logs)
}

func TestEventsAPI_RecoveredSuppressedInit_NoLineAndErrorCleared(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	// Cold-start init fails ...
	sendInit(t, e, "init", "error", "Runtime.Unknown", 5.0)
	assert.Equal(t, "Runtime.Unknown", e.InitErrorType())
	logs.reset()
	// ... and the suppressed init re-run at the first invocation recovers.
	sendInit(t, e, "invoke", "success", "", 6.0)

	// No INIT_REPORT for the successful re-run, and no Init Duration either
	// (AWS reports Init Duration only for the eager cold-start init phase).
	assert.Empty(t, logs.getLogs().Logs)
	_, ok := e.TakeColdStartInitDuration()
	assert.False(t, ok)

	// The failure record is per init attempt: the successful re-run resets it, so the
	// recovered invocation's REPORT is not tainted by the original failure — even if the
	// invocation itself later dies fatally.
	assert.Empty(t, e.InitErrorType())
}

func TestEventsAPI_RepeatedFailingSuppressedInit_ReRecordsEachAttempt(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	// Cold-start init fails, then every invocation re-runs the suppressed init,
	// which fails again — each attempt re-records its own error type.
	sendInit(t, e, "init", "error", "Runtime.Unknown", 5.0)
	sendInit(t, e, "invoke", "error", "Runtime.ExitError", 2.0)
	assert.Equal(t, "Runtime.ExitError", e.InitErrorType())
	sendInit(t, e, "invoke", "error", "Runtime.Unknown", 2.1)
	assert.Equal(t, "Runtime.Unknown", e.InitErrorType())
}

func TestEventsAPI_InvokeStart_EmitsStartLine(t *testing.T) {
	logs := NewLogCollector()
	e := NewLocalStackEventsAPI(logs, true)

	require.NoError(t, e.SendInvokeStart(interop.InvokeStartData{
		RequestID: "req-1",
		Version:   "$LATEST",
	}))

	assert.Equal(t, "START RequestId: req-1 Version: $LATEST\n", logs.getLogs().Logs)
}
