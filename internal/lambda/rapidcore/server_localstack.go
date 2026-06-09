package rapidcore

// This file contains LocalStack-specific additions to the rapidcore Server. It is kept
// separate from server.go (which is vendored upstream from
// aws-lambda-runtime-interface-emulator) so that upstream stays byte-identical and rebases
// never conflict. Because the logic needs the unexported init-failures channel and runtime
// state helpers, it must live in package rapidcore rather than in cmd/localstack.

import (
	"time"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/fatalerror"
	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lambda/interop"

	log "github.com/sirupsen/logrus"
)

// InitCompletionResponse carries the structured init failure cause (error type and message)
// extracted from the InitFailure. It lets standalone callers report the failure instead of
// only seeing the sentinel error (ErrInitDoneFailed / ErrInitResetReceived).
type InitCompletionResponse struct {
	InitErrorType    fatalerror.ErrorType
	InitErrorMessage error
}

// interpretInitFailure mirrors the upstream Server.awaitInitialized body, mapping an
// InitFailure to the sentinel error and structured cause. It is duplicated here (rather than
// refactored out of server.go) to keep the upstream file untouched.
func (s *Server) interpretInitFailure(initFailure interop.InitFailure, awaitingInitStatus bool) (InitCompletionResponse, error) {
	resp := InitCompletionResponse{}

	if initFailure.ResetReceived {
		// Resets during Init are only received in standalone
		// during an invoke timeout
		s.setRuntimeState(runtimeInitFailed)
		resp.InitErrorType = initFailure.ErrorType
		resp.InitErrorMessage = initFailure.ErrorMessage
		return resp, ErrInitResetReceived
	}

	if awaitingInitStatus {
		// channel not closed, received init failure
		// Sandbox can be reserved even if init failed (due to function errors)
		s.setRuntimeState(runtimeInitFailed)
		resp.InitErrorType = initFailure.ErrorType
		resp.InitErrorMessage = initFailure.ErrorMessage
		return resp, ErrInitDoneFailed
	}

	// not awaiting init status (channel closed)
	return resp, nil
}

// AwaitInitializedWithTimeout behaves like the upstream AwaitInitialized but (1) returns the
// structured init error on failure and (2) returns early if init does not complete within the
// timeout. On timeout it returns timedOut=true WITHOUT consuming the init-failures channel and
// without any side effects, so a subsequent invoke's Reserve()/awaitInitialized() can still
// observe the init outcome and trigger the suppressed init. The caller is expected to reset the
// in-progress init so that outcome becomes available.
func (s *Server) AwaitInitializedWithTimeout(timeout time.Duration) (resp InitCompletionResponse, timedOut bool, err error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		return InitCompletionResponse{}, true, nil
	case initFailure, awaitingInitStatus := <-s.getInitFailuresChan():
		resp, err = s.interpretInitFailure(initFailure, awaitingInitStatus)
		if err != nil {
			if releaseErr := s.Release(); releaseErr != nil {
				log.Infof("Error releasing after init failure %s: %s", err, releaseErr)
			}
			s.setRuntimeState(runtimeInitFailed)
			return resp, false, err
		}
		s.setRuntimeState(runtimeInitComplete)
		return resp, false, nil
	}
}
