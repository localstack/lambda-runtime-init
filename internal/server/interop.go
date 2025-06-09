package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/localstack/lambda-runtime-init/internal/localstack"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/directinvoke"
	"go.amzn.com/lambda/core/statejson"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/metering"
	"go.amzn.com/lambda/rapi/model"
	"go.amzn.com/lambda/rapidcore"
	"golang.org/x/sync/errgroup"
)

type LocalStackInteropsServer struct {
	*rapidcore.Server
	localStackAdapter *localstack.LocalStackClient
}

func NewInteropServer(server *rapidcore.Server, ls *localstack.LocalStackClient) *LocalStackInteropsServer {
	return &LocalStackInteropsServer{
		Server:            server,
		localStackAdapter: ls,
	}
}

func (c *LocalStackInteropsServer) Execute(ctx context.Context, responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Server.GetInvokeTimeout())
	defer cancel()

	if err := c.reserve(ctx, invoke); err != nil {
		return err
	}

	if err := c.executeInvoke(ctx, responseWriter, invoke); err != nil {
		return err
	}

	return nil
}

func (c *LocalStackInteropsServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	return c.Execute(context.Background(), responseWriter, invoke)
}

func (c *LocalStackInteropsServer) executeInvoke(ctx context.Context, responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		isDirect := directinvoke.MaxDirectResponseSize > interop.MaxPayloadSize
		err := c.Server.FastInvoke(responseWriter, invoke, isDirect)
		if err != nil {
			log.WithError(err).Debug("FastInvoke() failed")
		}
		return err
	})

	g.Go(func() error {
		_, err := c.AwaitRelease()
		return err
	})

	done := make(chan error, 1)
	go func() {
		done <- g.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-gCtx.Done():
		if errors.Is(gCtx.Err(), context.DeadlineExceeded) {
			if _, resetErr := c.Server.Reset("Timeout", 2000); resetErr != nil {
				log.WithError(resetErr).Errorf("Reset failed")
			}
			return rapidcore.ErrInvokeTimeout
		}
		return nil
	}
}

func (c *LocalStackInteropsServer) reserve(ctx context.Context, invoke *interop.Invoke) error {
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
				log.WithError(resetErr).Debug("Reset failed")
			}

			if _, err := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID); err != nil {
				return err
			}

			// If the original INIT failed, let's do another wait since we've triggered a RESERVE
			if err := c.Server.AwaitInitialized(); err != nil {
				return err
			}

			return nil
		default:
			return err
		}
	}

	return nil
}

func (c *LocalStackInteropsServer) AwaitRelease() (*statejson.ReleaseResponse, error) {
	resp, err := c.Server.AwaitRelease()
	switch err {
	case rapidcore.ErrReleaseReservationDone, nil:
		return resp, nil
	case rapidcore.ErrInitDoneFailed, rapidcore.ErrInvokeDoneFailed:
		if _, resetErr := c.Server.Reset("ReleaseFail", 2000); resetErr != nil {
			log.Errorf("Reset failed: %v", resetErr)
		}
		return nil, err
	default:
		if _, resetErr := c.Server.Reset("UnexpectedError", 2000); resetErr != nil {
			log.Errorf("Reset failed: %v", resetErr)
		}
		return nil, err
	}
}

func (c *LocalStackInteropsServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
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
