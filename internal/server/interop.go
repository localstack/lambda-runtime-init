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
		mutex:             &sync.Mutex{},
	}
}

type CustomInteropServer struct {
	*rapidcore.Server
	localStackAdapter *LocalStackAdapter
	mutex             *sync.Mutex
}

func (c *CustomInteropServer) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Server.GetInvokeTimeout())
	defer cancel()

	releaseRespChan := make(chan error)
	defer close(releaseRespChan)

	go func() {
		reserveResp, err := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID)
		if err != nil {
			releaseRespChan <- err
			return
		}

		invoke.DeadlineNs = fmt.Sprintf("%d", metering.Monotime()+reserveResp.Token.FunctionTimeout.Nanoseconds())

		// Wait for initialization to complete
		if err := c.Server.AwaitInitialized(); err != nil {
			switch err {
			case rapidcore.ErrInitDoneFailed:
				// Init failed, reset and continue with suppressed init
				if _, resetErr := c.Server.Reset("InitFailed", 2000); resetErr != nil {
					log.Errorf("Reset failed: %v", resetErr)
				}
				// Reserve again after reset for suppressed init
				if _, reserveErr := c.Server.Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID); reserveErr != nil {
					releaseRespChan <- reserveErr
					return
				}
			default:
				releaseRespChan <- err
				return
			}
		}

		go func() {
			isDirect := directinvoke.MaxDirectResponseSize > interop.MaxPayloadSize
			if err := c.Server.FastInvoke(responseWriter, invoke, isDirect); err != nil {
				log.Debugf("FastInvoke() error: %s", err)
			}
		}()

		_, err = c.Server.AwaitRelease()
		if err != nil {
			switch err {
			case rapidcore.ErrReleaseReservationDone:
				// Expected when Reset is called
				releaseRespChan <- nil
				return
			case rapidcore.ErrInitDoneFailed, rapidcore.ErrInvokeDoneFailed:
				if _, resetErr := c.Server.Reset("ReleaseFail", 2000); resetErr != nil {
					log.Errorf("Reset failed: %v", resetErr)
				}
				releaseRespChan <- err
				return
			default:
				if _, resetErr := c.Server.Reset("UnexpectedError", 2000); resetErr != nil {
					log.Errorf("Reset failed: %v", resetErr)
				}
				releaseRespChan <- err
				return
			}
		}

		releaseRespChan <- nil
	}()

	select {
	case err := <-releaseRespChan:
		return err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if _, resetErr := c.Server.Reset("Timeout", 2000); resetErr != nil {
				log.Errorf("Reset failed: %v", resetErr)
			}
			return rapidcore.ErrInvokeTimeout
		}
	}

	return nil
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
