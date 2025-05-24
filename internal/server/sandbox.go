package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/localstack/lambda-runtime-init/internal/logging"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/env"
)

type LocalStackAPI struct {
	shutdownCh chan struct{}

	sandbox   rapidcore.LambdaInvokeAPI
	bootstrap interop.Bootstrap
	// supervisor model.SupervisorClient

	logCollector *logging.LogCollector

	xrayEndpoint string // TODO replace with tracing config

	options      *LsOpts
	functionConf FunctionConfig
	awsConf      config.EnvConfig
}

func NewLocalStackAPI(
	sandbox rapidcore.LambdaInvokeAPI,
	bootstrap interop.Bootstrap,
	logCollector *logging.LogCollector,
	xrayEndpoint string,
	lsOpts *LsOpts,
	fnConf FunctionConfig,
	awsConf config.EnvConfig,
) *LocalStackAPI {
	return &LocalStackAPI{
		sandbox:   sandbox,
		bootstrap: bootstrap,

		logCollector: logCollector,

		xrayEndpoint: xrayEndpoint,
		options:      lsOpts,
		functionConf: fnConf,
		awsConf:      awsConf,

		shutdownCh: make(chan struct{}, 1),
	}
}

func (ls *LocalStackAPI) SendStatus(status LocalStackStatus, payload []byte) error {
	statusUrl := fmt.Sprintf("%s/status/%s/%s", ls.options.RuntimeEndpoint, ls.options.RuntimeId, status)
	_, err := http.Post(statusUrl, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return nil
}

func (ls *LocalStackAPI) Initialize() {
	additionalFunctionEnvironmentVariables := map[string]string{}

	// Forward Env Vars from the running system to what the function can view
	for _, env := range os.Environ() {
		envVar := strings.SplitN(env, "=", 2)
		if len(envVar) == 2 {
			additionalFunctionEnvironmentVariables[envVar[0]] = envVar[1]
		}
	}

	timeout, err := time.ParseDuration(ls.functionConf.FunctionTimeoutSec + "s")
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_TIMEOUT", ls.functionConf.FunctionTimeoutSec).
			Warnf("Failed to set function timeout from environment. Defaulting to 30s.")
		timeout = time.Second * 30
	}

	memorySize, err := strconv.ParseUint(ls.functionConf.FunctionMemorySizeMb, 10, 64)
	if err != nil {
		log.WithError(err).
			WithField("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", ls.functionConf.FunctionMemorySizeMb).
			Warnf("Failed to parse memory size. Defaulting to 128MB.")
		memorySize = 128
	}

	initRequest := &interop.Init{
		AccountID: ls.options.AccountId,

		InvokeTimeoutMs: timeout.Milliseconds(),
		InitTimeoutMs:   timeout.Milliseconds(),

		InstanceMaxMemory: memorySize,

		// TODO: LocalStack does not correctly set this to the LS container's <IP>:<PORT>
		XRayDaemonAddress: ls.xrayEndpoint,

		CustomerEnvironmentVariables: additionalFunctionEnvironmentVariables,
		EnvironmentVariables:         env.NewEnvironment(),

		SandboxType: interop.SandboxClassic,

		// TODO: Implement runtime management controls
		// https://aws.amazon.com/blogs/compute/introducing-aws-lambda-runtime-management-controls/
		RuntimeInfo: interop.RuntimeInfo{
			ImageJSON: "{}",
			Arn:       "",
			Version:   "",
		},
		Bootstrap: ls.bootstrap,
	}

	ls.sandbox.Init(initRequest, timeout.Milliseconds())
}

func (ls *LocalStackAPI) sendCallback(dest string, invokeId string, payload []byte) error {
	logger := log.WithField("invoke-id", invokeId).WithField("destination", dest)
	endpoint, err := url.JoinPath(ls.options.RuntimeEndpoint, "invocations", invokeId, dest)
	if err != nil {
		logger.WithError(err).Error("Failed to construct a callback URL.")
	}

	logger.Infof("Sending callback")
	if _, err := http.Post(endpoint, "application/json", bytes.NewReader(payload)); err != nil {
		logger.WithError(err).Error("Failed to send callback to LocalStack")
	}

	return nil
}

func (ls *LocalStackAPI) CollectAndSendLogs(invokeId string) error {
	serializedLogs, err := json.Marshal(ls.logCollector.GetLogs())
	if err != nil {
		return err
	}

	return ls.sendCallback("/logs", invokeId, serializedLogs)
}

func (ls *LocalStackAPI) SendError(invokeId string, payload []byte) error {
	return ls.sendCallback("/error", invokeId, payload)
}

func (ls *LocalStackAPI) SendResponse(invokeId string, payload []byte) error {
	return ls.sendCallback("/response", invokeId, payload)

}

func (ls *LocalStackAPI) Init(i *interop.Init, invokeTimeoutMs int64) {
	ls.Init(i, invokeTimeoutMs)
}

func (ls *LocalStackAPI) Invoke(responseWriter http.ResponseWriter, invoke *interop.Invoke) error {
	_, _ = fmt.Fprintf(ls.logCollector, "START RequestId: %s Version: %s\n", invoke.ID, ls.functionConf.FunctionVersion)

	fnArn := arn.ARN{
		Partition: "aws",
		Service:   "lambda",
		Region:    ls.awsConf.Region,
		AccountID: ls.options.AccountId,
		Resource:  fmt.Sprintf("%s:%s", ls.functionConf.FunctionName, ls.functionConf.FunctionVersion),
	}

	invoke.NeedDebugLogs = true
	invoke.InvokedFunctionArn = fnArn.String()

	err := ls.sandbox.Invoke(responseWriter, invoke)
	if err != nil {
		log.WithError(err).Error("Failed invocation.")

	}

	invokeStartTime := invoke.InvokeReceivedTime
	invokeEndTime := invoke.InvokeResponseMetrics.FinishReadingResponseMonoTimeMs

	durationMs := float64(invokeEndTime-invokeStartTime) / float64(time.Millisecond)

	report := InvokeReport{
		InvokeId:         invoke.ID,
		DurationMs:       durationMs,
		BilledDurationMs: math.Ceil(durationMs),
		MemorySizeMB:     ls.functionConf.FunctionMemorySizeMb,
		MaxMemoryUsedMB:  ls.functionConf.FunctionMemorySizeMb,
	}

	if pErr := report.Print(ls.logCollector); pErr != nil {
		log.WithError(pErr).Error("Failed to write END report.")
	}

	return err
}

func (ls *LocalStackAPI) AfterInvoke() error {
	// optional sleep. can be used for debugging purposes
	if ls.options.PostInvokeWaitMS != "" {
		waitMS, err := strconv.Atoi(ls.options.PostInvokeWaitMS)
		if err != nil {
			return err
		}
		select {
		case <-time.After(time.Duration(waitMS) * time.Millisecond):
		case <-ls.shutdownCh:
			log.Debug("Post invocation delay cancelled because shutdown was triggered.")
		}
	}

	return nil
}

func (ls *LocalStackAPI) Close() {
	log.Debug("Shutdown of LocalStackAPI triggered.")
	close(ls.shutdownCh)
}
