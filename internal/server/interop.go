package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/core/directinvoke"
	"go.amzn.com/lambda/fatalerror"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/metering"
	"go.amzn.com/lambda/rapi/model"
	"go.amzn.com/lambda/rapidcore"
)

type LocalStackAdapter struct {
	UpstreamEndpoint string
	RuntimeId        string
}

func NewLocalStackAdapter(endpoint, runtimeId string) *LocalStackAdapter {
	return &LocalStackAdapter{
		UpstreamEndpoint: endpoint,
		RuntimeId:        runtimeId,
	}
}

func (ls *LocalStackAdapter) sendCallback(namespace, invokeId, dest string, payload []byte) error {

	endpoint, err := url.JoinPath(ls.UpstreamEndpoint, namespace, invokeId, dest)
	if err != nil {
		return err
	}
	logrus.WithField("url", endpoint).WithField("invoke-id", invokeId).Debugf("Sending callback.")

	if _, err := http.Post(endpoint, "application/json", bytes.NewReader(payload)); err != nil {
		return err
	}

	return nil
}

func (ls *LocalStackAdapter) SendStatus(status LocalStackStatus, payload []byte) error {
	return ls.sendCallback("/status", ls.RuntimeId, string(status), payload)
}

func (ls *LocalStackAdapter) SendInvocation(invokeId, dest string, payload []byte) error {
	return ls.sendCallback("invocations", invokeId, dest, payload)
}

func NewInteropServer(server *rapidcore.Server, ls *LocalStackAdapter) *CustomInteropServer {
	return &CustomInteropServer{
		Server:            server,
		localStackAdapter: ls,
	}
}

type CustomInteropServer struct {
	*rapidcore.Server
	localStackAdapter *LocalStackAdapter
	initOnce          sync.Once
}

func (c *CustomInteropServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Server.GetInvokeTimeout())
	defer cancel()

	releaseRespChan := make(chan error)
	defer close(releaseRespChan)

	go func() {
		reserveResp, err := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID)
		if err != nil && !errors.Is(err, rapidcore.ErrAlreadyReserved) {
			releaseRespChan <- err
			return
		}

		invoke.DeadlineNs = fmt.Sprintf("%d", metering.Monotime()+reserveResp.Token.FunctionTimeout.Nanoseconds())
		go func() {
			isDirect := directinvoke.MaxDirectResponseSize > interop.MaxPayloadSize
			if err := c.Server.FastInvoke(responseWriter, invoke, isDirect); err != nil {
				log.Debugf("FastInvoke() error: %s", err)
			}
		}()

		// Waits for an invocation to finish before calling Release()
		_, err = c.Server.AwaitRelease()
		if err != nil {
			log.Debugf("AwaitRelease() error: %s", err)
			switch err {
			case rapidcore.ErrReleaseReservationDone:
				// not an error, expected return value when Reset is called
				return
			case rapidcore.ErrInitDoneFailed, rapidcore.ErrInvokeDoneFailed:
				c.Server.Reset("ReleaseFail", 2000)
			}
		}
		releaseRespChan <- err
	}()

	select {
	case err := <-releaseRespChan:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.Server.Reset("Timeout", 2000)
			return rapidcore.ErrInvokeTimeout
		}
	}

	return c.Server.Release()
}

func (c *CustomInteropServer) AwaitInitialized() error {
	err := c.Server.AwaitInitialized()
	go func() {
		state, stateErr := c.InternalState()
		if stateErr != nil {
			return
		}

		doneResponse := &interop.Done{}
		if state.FirstFatalError != "" {
			doneResponse.ErrorType = fatalerror.GetValidRuntimeOrFunctionErrorType(state.FirstFatalError)
		}

		c.InitDoneChan <- rapidcore.DoneWithState{
			Done:  doneResponse,
			State: c.InternalStateGetter(),
		}
	}()
	return err
}

func (c *CustomInteropServer) SendInitErrorResponse(resp *interop.ErrorInvokeResponse) error {
	errResp := &model.ErrorResponse{}
	err := json.Unmarshal(resp.Payload, errResp)
	if err != nil {
		return err
	}

	adaptedErroResp := ErrorResponse{
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
		if err := c.localStackAdapter.SendStatus(Error, body); err != nil {
			log.WithError(err).WithField("runtime-id", c.localStackAdapter.RuntimeId).Error("Failed to send response callback")
		}
	}()

	return c.Server.SendInitErrorResponse(resp)
}
