package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/metering"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/env"
)

type LocalStackService struct {
	shutdownCh chan struct{}

	sandbox *CustomInteropServer
	adapter *LocalStackAdapter
	// supervisor model.SupervisorClient

	logCollector *logging.LogCollector

	xrayEndpoint string // TODO replace with tracing config

	runtime  *LsOpts
	function FunctionConfig
	aws      config.EnvConfig

	initDuration float64
}

func NewLocalStackService(
	sandbox *CustomInteropServer,
	logCollector *logging.LogCollector,
	adapter *LocalStackAdapter,
	xrayEndpoint string,
	runtime *LsOpts,
	fnConf FunctionConfig,
	awsConf config.EnvConfig,
) *LocalStackService {
	return &LocalStackService{
		sandbox:      sandbox,
		adapter:      adapter,
		logCollector: logCollector,

		xrayEndpoint: xrayEndpoint,

		function: fnConf,
		aws:      awsConf,
		runtime:  runtime,

		shutdownCh: make(chan struct{}, 1),
	}
}

func (ls *LocalStackService) Initialize(bs interop.Bootstrap) error {
	timeout, err := time.ParseDuration(ls.function.FunctionTimeoutSec + "s")
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_TIMEOUT", ls.function.FunctionTimeoutSec).
			Warnf("Failed to set function timeout from environment. Defaulting to 30s.")
		timeout = time.Second * 30
	}

	memorySize, err := strconv.ParseUint(ls.function.FunctionMemorySizeMb, 10, 64)
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", ls.function.FunctionMemorySizeMb).
			Warnf("Failed to parse memory size. Defaulting to 128MB.")
		memorySize = 128
	}

	initRequest := &interop.Init{
		AccountID:         ls.aws.Credentials.AccountID,
		Handler:           ls.function.FunctionHandler,
		InvokeTimeoutMs:   timeout.Milliseconds(),
		InitTimeoutMs:     timeout.Milliseconds(),
		InstanceMaxMemory: memorySize,
		// TODO: LocalStack does not correctly set this to the LS container's <IP>:<PORT>
		XRayDaemonAddress: ls.xrayEndpoint,

		EnvironmentVariables:         env.NewEnvironment(),
		CustomerEnvironmentVariables: env.CustomerEnvironmentVariables(),
		SandboxType:                  interop.SandboxClassic,

		// TODO: Implement runtime management controls
		// https://aws.amazon.com/blogs/compute/introducing-aws-lambda-runtime-management-controls/
		RuntimeInfo: interop.RuntimeInfo{
			ImageJSON: "{}",
			Arn:       "",
			Version:   "",
		},
		Bootstrap: bs,
	}

	initStart := metering.Monotime()
	err = ls.sandbox.Init(initRequest, timeout.Milliseconds())
	ls.initDuration = float64(metering.Monotime()-initStart) / float64(time.Millisecond)
	return err
}

func (ls *LocalStackService) SendStatus(status LocalStackStatus, payload []byte) error {
	return ls.adapter.SendStatus(status, payload)
}

func (ls *LocalStackService) CollectAndSendLogs(invokeId string) error {
	serializedLogs, err := json.Marshal(ls.logCollector.GetLogs())
	if err != nil {
		return err
	}
	return ls.adapter.SendInvocation(invokeId, "/logs", serializedLogs)
}

func (ls *LocalStackService) SendError(invokeId string, payload []byte) error {
	return ls.adapter.SendInvocation(invokeId, "/error", payload)
}

func (ls *LocalStackService) SendResponse(invokeId string, payload []byte) error {
	return ls.adapter.SendInvocation(invokeId, "/response", payload)
}

func (ls *LocalStackService) InvokeForward(invoke InvokeRequest) ([]byte, error) {
	proxyResponseWriter := NewResponseWriterProxy()

	_, _ = fmt.Fprintf(ls.logCollector, "START RequestId: %s Version: %s\n", invoke.InvokeId, ls.function.FunctionVersion)

	clientContext, err := base64.StdEncoding.DecodeString(invoke.ClientContext)
	if err != nil {
		log.WithError(err).Debug("client context was not valid base64, defaulting to plain-string: %s", clientContext)
	}

	// start := metering.Monotime()
	invokePayload := &interop.Invoke{
		ID:                 invoke.InvokeId,
		InvokedFunctionArn: invoke.InvokedFunctionArn,
		TraceID:            invoke.TraceId,
		Payload:            strings.NewReader(invoke.Payload),
		ClientContext:      string(clientContext),
		NeedDebugLogs:      true,
	}

	var invokeErr error
	if invokeErr = ls.sandbox.Invoke(proxyResponseWriter, invokePayload); invokeErr != nil {
		log.WithError(invokeErr).Error("Failed invocation.")
	}

	invokeStartTime := invokePayload.InvokeReceivedTime
	invokeEndTime := metering.Monotime()

	durationMs := float64(invokeEndTime-invokeStartTime) / float64(time.Millisecond)

	report := InvokeReport{
		InvokeId:         invoke.InvokeId,
		DurationMs:       durationMs,
		BilledDurationMs: math.Ceil(durationMs),
		MemorySizeMB:     ls.function.FunctionMemorySizeMb,
		MaxMemoryUsedMB:  ls.function.FunctionMemorySizeMb,
		InitDurationMs:   ls.initDuration,
	}

	switch invokeErr {
	case rapidcore.ErrInvokeTimeout:
		report.Status = "timeout"
	}

	if err := report.Print(ls.logCollector); err != nil {
		log.WithError(err).Error("Failed to write END report.")
	}

	return proxyResponseWriter.Body(), invokeErr
}

func (ls *LocalStackService) AfterInvoke(ctx context.Context) error {
	// optional sleep. can be used for debugging purposes
	if ls.runtime.PostInvokeWaitMS != "" {
		waitMS, err := strconv.Atoi(ls.runtime.PostInvokeWaitMS)
		if err != nil {
			return err
		}
		select {
		case <-time.After(time.Duration(waitMS) * time.Millisecond):
		case <-ls.shutdownCh:
			log.Debug("Post invocation delay cancelled because shutdown was triggered.")
		case <-ctx.Done():
			log.Debug("Post invocation delay cancelled because request was cancelled.")
		}
	}

	return nil
}

func (ls *LocalStackService) Close() {
	log.Debug("Shutdown of LocalStackService triggered.")
	close(ls.shutdownCh)
}
