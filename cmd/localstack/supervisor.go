package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/fatalerror"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/supervisor"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/supervisor/model"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// LocalStackSupervisor wraps a ProcessSupervisor and intercepts runtime process termination events.
// When a runtime process exits unexpectedly it sends a fault event via the EventsAPI so LocalStack
// receives a proper error instead of timing out.
type LocalStackSupervisor struct {
	model.ProcessSupervisor
	eventsChan chan model.Event
	eventsAPI  interop.EventsAPI

	isShuttingDown *atomic.Bool
}

func NewLocalStackSupervisor(ctx context.Context, evs interop.EventsAPI) *LocalStackSupervisor {
	var isShuttingDown atomic.Bool
	ls := &LocalStackSupervisor{
		ProcessSupervisor: supervisor.NewLocalSupervisor(),
		eventsAPI:         evs,
		eventsChan:        make(chan model.Event),
		isShuttingDown:    &isShuttingDown,
	}

	go ls.loop(ctx)

	return ls
}

func (ls *LocalStackSupervisor) loop(ctx context.Context) {
	inCh, err := ls.ProcessSupervisor.Events(ctx, nil)
	if err != nil {
		panic(err)
	}
	defer close(ls.eventsChan)
	for {
		select {
		case event, ok := <-inCh:
			if !ok {
				return
			}

			select {
			case ls.eventsChan <- event:
			case <-ctx.Done():
				return
			}

			if ls.isShuttingDown.Load() {
				continue
			}

			termination := event.Event.ProcessTerminated()
			if termination == nil {
				continue
			}

			if !strings.Contains(*termination.Name, "runtime-") {
				log.Debugf("Ignoring non-runtime process termination: %s", *termination.Name)
				continue
			}

			if termination.Signaled() != nil {
				log.Debugf("Runtime process signalled: %d", *termination.Signo)
			}

			faultData := interop.FaultData{
				RequestID:    interop.RequestID(uuid.NewString()),
				ErrorMessage: errors.New("Runtime exited without providing a reason"),
				ErrorType:    fatalerror.RuntimeExit,
			}
			if !termination.Success() {
				faultData.ErrorMessage = fmt.Errorf("Runtime exited with error: %s", termination.String())
			}

			if err := ls.eventsAPI.SendFault(faultData); err != nil {
				log.WithError(err).Error("Failed to send runtime fault event")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (ls *LocalStackSupervisor) Exec(ctx context.Context, request *model.ExecRequest) error {
	if request.Domain == "runtime" {
		ls.isShuttingDown.Store(false)
	}
	return ls.ProcessSupervisor.Exec(ctx, request)
}

func (ls *LocalStackSupervisor) Terminate(ctx context.Context, request *model.TerminateRequest) error {
	defer func() {
		if request.Domain == "runtime" && strings.HasPrefix(request.Name, "runtime-") {
			ls.isShuttingDown.Store(true)
		}
	}()
	return ls.ProcessSupervisor.Terminate(ctx, request)
}

func (ls *LocalStackSupervisor) Kill(ctx context.Context, request *model.KillRequest) error {
	defer func() {
		if request.Domain == "runtime" && strings.HasPrefix(request.Name, "runtime-") {
			ls.isShuttingDown.Store(true)
		}
	}()
	return ls.ProcessSupervisor.Kill(ctx, request)
}

func (ls *LocalStackSupervisor) Events(ctx context.Context, _ *model.EventsRequest) (<-chan model.Event, error) {
	return ls.eventsChan, nil
}
