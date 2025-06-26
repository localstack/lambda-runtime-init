package tracing

import (
	"context"
	"sync"

	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/telemetry"
)

type LocalStackTracer struct {
	telemetry.Tracer
	mu     *sync.RWMutex
	invoke *interop.Invoke
}

func NewLocalStackTracer() *LocalStackTracer {
	return &LocalStackTracer{
		Tracer: telemetry.NewNoOpTracer(),
		mu:     &sync.RWMutex{},
	}
}

func (t *LocalStackTracer) Configure(invoke *interop.Invoke) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.invoke = invoke
}

func (t *LocalStackTracer) BuildTracingHeader() func(context.Context) string {
	// extract root trace ID and parent from context and build the tracing header
	return func(ctx context.Context) string {
		t.mu.RLock()
		defer t.mu.RUnlock()
		return t.invoke.TraceID
	}
}
