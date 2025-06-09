package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/localstack/lambda-runtime-init/internal/aws/lambda"
	"github.com/localstack/lambda-runtime-init/internal/localstack"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	"github.com/localstack/lambda-runtime-init/internal/supervisor"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/metering"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/env"
)

type LocalStackService struct {
	sandbox *LocalStackInteropsServer
	adapter *localstack.LocalStackClient
	supv    *supervisor.LocalStackSupervisor

	logCollector *logging.LogCollector

	xrayEndpoint string // TODO replace with tracing config

	runtime  *localstack.Config
	function lambda.FunctionConfig
	aws      config.EnvConfig

	initDuration float64

	pendingCallbacks int64
	isShuttingDown   *atomic.Bool
	allDone          chan struct{}
}

func NewLocalStackService(
	sandbox *LocalStackInteropsServer,
	logCollector *logging.LogCollector,
	adapter *localstack.LocalStackClient,
	supv *supervisor.LocalStackSupervisor,
	xrayEndpoint string,
	runtime *localstack.Config,
	fnConf lambda.FunctionConfig,
	awsConf config.EnvConfig,
) *LocalStackService {
	return &LocalStackService{
		sandbox:      sandbox,
		adapter:      adapter,
		logCollector: logCollector,
		supv:         supv,

		xrayEndpoint: xrayEndpoint,

		function: fnConf,
		aws:      awsConf,
		runtime:  runtime,

		allDone:          make(chan struct{}, 1),
		isShuttingDown:   &atomic.Bool{},
		pendingCallbacks: 0,
	}
}

func (ls *LocalStackService) Initialize(bs interop.Bootstrap) error {
	initTimeout := time.Second * 10
	invokeTimeout, err := time.ParseDuration(ls.function.FunctionTimeoutSec + "s")
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_TIMEOUT", ls.function.FunctionTimeoutSec).
			Warnf("Failed to set function timeout from environment. Defaulting to 30s.")
		invokeTimeout = time.Second * 30
	}

	memorySize, err := strconv.ParseUint(ls.function.FunctionMemorySizeMb, 10, 64)
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", ls.function.FunctionMemorySizeMb).
			Warnf("Failed to parse memory size. Defaulting to 128MB.")
		memorySize = 128
	}

	customerEnvironmentVariables := map[string]string{}
	// Cant use env.CustomerEnvironmentVariables() since this strips internal envars (like creds)
	// from the variables passed to the process.
	for _, e := range os.Environ() {
		key, val, err := env.SplitEnvironmentVariable(e)
		if err != nil {
			continue
		}
		customerEnvironmentVariables[key] = val
	}

	initRequest := &interop.Init{
		AccountID:         ls.aws.Credentials.AccountID,
		Handler:           ls.function.FunctionHandler,
		InvokeTimeoutMs:   invokeTimeout.Milliseconds(),
		InitTimeoutMs:     initTimeout.Milliseconds(),
		InstanceMaxMemory: memorySize,
		// TODO: LocalStack does not correctly set this to the LS container's <IP>:<PORT>
		XRayDaemonAddress: ls.xrayEndpoint,

		AwsKey:     ls.aws.Credentials.AccessKeyID,
		AwsSecret:  ls.aws.Credentials.SecretAccessKey,
		AwsSession: ls.aws.Credentials.SessionToken,

		EnvironmentVariables:         env.NewEnvironment(),
		CustomerEnvironmentVariables: customerEnvironmentVariables,
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
	err = ls.sandbox.Init(initRequest, initRequest.InvokeTimeoutMs)
	ls.initDuration = float64(metering.Monotime()-initStart) / float64(time.Millisecond)

	if err != nil {
		_, _ = fmt.Fprintf(ls.logCollector, "INIT_REPORT Init Duration: %.2f ms Phase: init Status: %s", ls.initDuration, err.Error())
	}

	return err
}

func (ls *LocalStackService) ForwardLogs(invokeId string) error {
	if ls.isShuttingDown.Load() {
		if atomic.LoadInt64(&ls.pendingCallbacks) == 0 {
			close(ls.allDone)
		}
		return nil
	}

	atomic.AddInt64(&ls.pendingCallbacks, 1)
	defer atomic.AddInt64(&ls.pendingCallbacks, -1)

	serializedLogs, err := json.Marshal(ls.logCollector.GetLogs())
	if err != nil {
		return err
	}
	return ls.adapter.SendInvocation(invokeId, "/logs", serializedLogs)
}

func (ls *LocalStackService) SendStatus(status localstack.LocalStackStatus, payload []byte) error {
	if ls.isShuttingDown.Load() {
		if atomic.LoadInt64(&ls.pendingCallbacks) == 0 {
			close(ls.allDone)
		}
		return nil
	}

	atomic.AddInt64(&ls.pendingCallbacks, 1)
	defer atomic.AddInt64(&ls.pendingCallbacks, -1)

	return ls.adapter.SendStatus(status, payload)
}

func (ls *LocalStackService) SendError(invokeId string, payload []byte) error {
	if ls.isShuttingDown.Load() {
		if atomic.LoadInt64(&ls.pendingCallbacks) == 0 {
			close(ls.allDone)
		}
		return nil
	}

	atomic.AddInt64(&ls.pendingCallbacks, 1)
	defer atomic.AddInt64(&ls.pendingCallbacks, -1)

	return ls.adapter.SendInvocation(invokeId, "/error", payload)
}

func (ls *LocalStackService) SendResponse(invokeId string, payload []byte) error {
	atomic.AddInt64(&ls.pendingCallbacks, 1)
	defer atomic.AddInt64(&ls.pendingCallbacks, -1)

	return ls.adapter.SendInvocation(invokeId, "/response", payload)
}

func (ls *LocalStackService) InvokeForward(ctx context.Context, invoke localstack.InvokeRequest) ([]byte, error) {
	proxyResponseWriter := NewResponseWriterProxy()

	_, _ = fmt.Fprintf(ls.logCollector, "START RequestId: %s Version: %s\n", invoke.InvokeId, ls.function.FunctionVersion)

	clientContext, err := base64.StdEncoding.DecodeString(invoke.ClientContext)
	if err != nil {
		log.WithError(err).Debug("client context was not valid base64, defaulting to plain-string: %s", clientContext)
	}

	invokePayload := &interop.Invoke{
		ID:                 invoke.InvokeId,
		InvokedFunctionArn: invoke.InvokedFunctionArn,
		TraceID:            invoke.TraceId,
		Payload:            strings.NewReader(invoke.Payload),
		ClientContext:      string(clientContext),
		NeedDebugLogs:      true,
	}

	invokeErr := ls.sandbox.Execute(ctx, proxyResponseWriter, invokePayload)
	if invokeErr != nil {
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
		case <-ctx.Done():
			log.Debug("Post invocation delay cancelled because request was cancelled.")
		}
	}

	return nil
}

func (ls *LocalStackService) Close() {
	ls.isShuttingDown.Store(true)
	log.Debug("Shutdown of LocalStackService triggered.")
}

func (ls *LocalStackService) shutdownTriggered() bool {
	return ls.isShuttingDown.Load()
}

func (ls *LocalStackService) AwaitCompleted(ctx context.Context) error {
	if !ls.isShuttingDown.Load() {
		return fmt.Errorf("Shutdown has not been triggered.")
	}

	for {
		select {
		case <-ctx.Done():
			pending := atomic.LoadInt64(&ls.pendingCallbacks)
			return fmt.Errorf("failed to gracefully complete all callbacks to LocalStack: %d remaining", pending)
		case <-ls.allDone:
			if atomic.LoadInt64(&ls.pendingCallbacks) == 0 {
				return nil
			}
		}
	}
}
