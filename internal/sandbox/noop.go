package sandbox

import "go.amzn.com/lambda/interop"

var _ interop.SandboxContext = (*noopSandboxCtx)(nil)

type noopSandboxCtx struct{}

func NewNoopSandboxContext() *noopSandboxCtx {
	return &noopSandboxCtx{}
}

func (ctx *noopSandboxCtx) Init(_ *interop.Init, _ int64) interop.InitContext {
	return nil
}

func (ctx *noopSandboxCtx) Reset(_ *interop.Reset) (interop.ResetSuccess, *interop.ResetFailure) {
	return interop.ResetSuccess{}, nil
}

func (ctx *noopSandboxCtx) Shutdown(_ *interop.Shutdown) interop.ShutdownSuccess {
	return interop.ShutdownSuccess{}
}

func (ctx *noopSandboxCtx) Restore(_ *interop.Restore) (interop.RestoreResult, error) {
	return interop.RestoreResult{}, nil
}

func (ctx *noopSandboxCtx) SetRuntimeStartedTime(_ int64) {}

func (ctx *noopSandboxCtx) SetInvokeResponseMetrics(_ *interop.InvokeResponseMetrics) {}
