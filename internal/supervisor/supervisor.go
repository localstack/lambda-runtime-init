package supervisor

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/statejson"
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
}

func NewLocalStackSupervisor(ctx context.Context, supv model.ProcessSupervisor, evs interop.EventsAPI, getState func() (*statejson.InternalStateDescription, error)) *LocalStackSupervisor {
	inCh, err := supv.Events(ctx, nil)
	if err != nil {
		panic(err)
	}
	eventCh := make(chan model.Event)

	go func() {
		defer close(eventCh)
		for {
			select {
			case event, ok := <-inCh:
				if !ok {
					return
				}

				select {
				case eventCh <- event:
				case <-ctx.Done():
					return
				}

				termination := event.Event.ProcessTerminated()
				if termination == nil {
					continue
				}

				if !strings.Contains(*termination.Name, "runtime-") {
					logrus.Infof("Ignoring non-runtime process termination: %s", *termination.Name)
					continue
				}

				_, err := getState()
				if err != nil {
					logrus.Errorf("Failed to get state: %v", err)
					continue
				}

				if termination.Signaled() != nil {
					logrus.Infof("SIGNALLED: %d", *termination.Signo)
				}

				faultData := interop.FaultData{
					RequestID:    interop.RequestID(uuid.NewString()), // Have not gotten invoke ctx, so we don't have the requestID
					ErrorMessage: fmt.Errorf("Runtime exited without providing a reason"),
					ErrorType:    fatalerror.RuntimeExit,
				}
				if !termination.Success() {
					faultData.ErrorMessage = fmt.Errorf("Runtime exited with error: %s", termination.String())
				}

				evs.SendFault(faultData)
			case <-ctx.Done():
				return
			}
		}
	}()

	return &LocalStackSupervisor{
		ProcessSupervisor: supv,
		eventsChan:        eventCh,
	}

}

func (ls *LocalStackSupervisor) Events(ctx context.Context, _ *model.EventsRequest) (<-chan model.Event, error) {
	return ls.eventsChan, nil
}
