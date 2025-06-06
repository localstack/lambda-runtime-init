package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/localstack/lambda-runtime-init/internal/localstack"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/directinvoke"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/metering"
	"go.amzn.com/lambda/rapi/model"
	"go.amzn.com/lambda/rapidcore"
	"golang.org/x/sync/errgroup"
)

type CustomInteropServer struct {
	*rapidcore.Server
	localStackAdapter *localstack.LocalStackClient
	mutex             *sync.Mutex
}

func NewInteropServer(server *rapidcore.Server, ls *localstack.LocalStackClient) *CustomInteropServer {
	return &CustomInteropServer{
		Server:            server,
		localStackAdapter: ls,
		mutex:             &sync.Mutex{},
	}
}

func (c *CustomInteropServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Server.GetInvokeTimeout())
	defer cancel()

	if err := c.reserveForInvoke(ctx, invoke); err != nil {
		return err
	}

	return c.executeInvoke(ctx, responseWriter, invoke)
}

func (c *CustomInteropServer) executeInvoke(ctx context.Context, responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		isDirect := directinvoke.MaxDirectResponseSize > interop.MaxPayloadSize
		if err := c.Server.FastInvoke(responseWriter, invoke, isDirect); err != nil {
			log.Debugf("FastInvoke() error: %s", err)
		}
		return nil
	})

	g.Go(func() error {
		_, err := c.Server.AwaitRelease()
		if err != nil {
			return c.handleReleaseError(err)
		}
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- g.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-gCtx.Done():
		return c.handleTimeout()
	}
}

func (c *CustomInteropServer) reserveForInvoke(ctx context.Context, invoke *interop.Invoke) error {
	reserveResp, err := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID)
	if err != nil {
		return err
	}

	invoke.DeadlineNs = fmt.Sprintf("%d", metering.Monotime()+reserveResp.Token.FunctionTimeout.Nanoseconds())

	// From https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtime-environment.html
	// If the first INIT times out, Lambda retries the Init phase on first INVOKE.
	if err := c.Server.AwaitInitialized(); err != nil {
		switch err {
		case rapidcore.ErrInitDoneFailed:
			if _, resetErr := c.Server.Reset("InitFailed", 2000); resetErr != nil {
				log.Errorf("Reset failed: %v", resetErr)
			}

			if _, reserveErr := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID); reserveErr != nil {
				return reserveErr
			}
			return nil
		default:
			return err
		}
	}

	return nil
}

func (c *CustomInteropServer) handleReleaseError(err error) error {
	switch err {
	case rapidcore.ErrReleaseReservationDone:
		return nil
	case rapidcore.ErrInitDoneFailed, rapidcore.ErrInvokeDoneFailed:
		if _, resetErr := c.Server.Reset("ReleaseFail", 2000); resetErr != nil {
			log.Errorf("Reset failed: %v", resetErr)
		}
		return err
	default:
		if _, resetErr := c.Server.Reset("UnexpectedError", 2000); resetErr != nil {
			log.Errorf("Reset failed: %v", resetErr)
		}
		return err
	}
}

func (c *CustomInteropServer) handleTimeout() error {
	if _, resetErr := c.Server.Reset("Timeout", 2000); resetErr != nil {
		log.Errorf("Reset failed: %v", resetErr)
	}
	return rapidcore.ErrInvokeTimeout
}

func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
	errResp := &model.ErrorResponse{}
	err := json.Unmarshal(resp.Payload, errResp)
	if err != nil {
		return err
	}

	adaptedErroResp := localstack.ErrorResponse{
		ErrorMessage: errResp.ErrorMessage,
		RequestId:    aws.String(c.GetCurrentInvokeID()),
		ErrorType:    errResp.ErrorType,
		StackTrace:   errResp.StackTrace,
	}

	body, err := json.Marshal(adaptedErroResp)
	if err != nil {
		return err
	}

	go func() {
		if err := c.localStackAdapter.SendStatus(localstack.Error, body); err != nil {
			log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).Error("Failed to send response callback")
		}
	}()

	return c.Server.SendInitErrorResponse(resp)
}
