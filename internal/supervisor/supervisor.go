package supervisor

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"go.amzn.com/lambda/fatalerror"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/supervisor/model"
)

// LocalStackSupervisor wraps a pre-existing ProcessSupervisor and intercepts all emitted Events.
// This allows us to observe and customize the handling of a process' lifecycle.
// Currently, this is used to extract information about the exit codes of a process in the event of a SIGKILL.
type LocalStackSupervisor struct {
	model.ProcessSupervisor
	eventsChan chan model.Event
	eventsAPI  interop.EventsAPI

	isShuttingDown *atomic.Bool
}

func NewLocalStackSupervisor(ctx context.Context, supv model.ProcessSupervisor, evs interop.EventsAPI) *LocalStackSupervisor {
	var isShuttingDown atomic.Bool
	ls := &LocalStackSupervisor{
		ProcessSupervisor: supv,
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
				logrus.Debugf("Ignoring non-runtime process termination: %s", *termination.Name)
				continue
			}

			if termination.Signaled() != nil {
				logrus.Debugf("SIGNALLED: %d", *termination.Signo)
			}

			faultData := interop.FaultData{
				RequestID:    interop.RequestID(uuid.NewString()), // Have not gotten invoke ctx, so we don't have the requestID
				ErrorMessage: fmt.Errorf("Runtime exited without providing a reason"),
				ErrorType:    fatalerror.RuntimeExit,
			}
			if !termination.Success() {
				faultData.ErrorMessage = fmt.Errorf("Runtime exited with error: %s", termination.String())
			}

			ls.eventsAPI.SendFault(faultData)
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
