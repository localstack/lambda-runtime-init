package server

import (
	"os"
	"strings"
	"time"

	"github.com/localstack/lambda-runtime-init/lambda/utils"
	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
	"go.amzn.com/lambda/rapidcore/env"
)

func InitHandler(sandbox rapidcore.LambdaInvokeAPI, functionVersion string, timeout int64, bs interop.Bootstrap, accountId string) (time.Time, time.Time) {
	additionalFunctionEnvironmentVariables := map[string]string{}

	// Add default Env Vars if they were not defined. This is a required otherwise 1p Python2.7, Python3.6, and
	// possibly others pre runtime API runtimes will fail. This will be overwritten if they are defined on the system.
	additionalFunctionEnvironmentVariables["AWS_LAMBDA_LOG_GROUP_NAME"] = "/aws/lambda/Functions"
	additionalFunctionEnvironmentVariables["AWS_LAMBDA_LOG_STREAM_NAME"] = "$LATEST"
	additionalFunctionEnvironmentVariables["AWS_LAMBDA_FUNCTION_VERSION"] = "$LATEST"
	additionalFunctionEnvironmentVariables["AWS_LAMBDA_FUNCTION_MEMORY_SIZE"] = "3008"
	additionalFunctionEnvironmentVariables["AWS_LAMBDA_FUNCTION_NAME"] = "test_function"

	// Forward Env Vars from the running system (container) to what the function can view. Without this, Env Vars will
	// not be viewable when the function runs.
	for _, env := range os.Environ() {
		// Split the env into by the first "=". This will account for if the env var's value has a '=' in it
		envVar := strings.SplitN(env, "=", 2)
		additionalFunctionEnvironmentVariables[envVar[0]] = envVar[1]
	}

	initStart := time.Now()
	// pass to rapid
	sandbox.Init(&interop.Init{
		Handler:           utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_HANDLER", os.Getenv("_HANDLER")),
		AwsKey:            os.Getenv("AWS_ACCESS_KEY_ID"),
		AwsSecret:         os.Getenv("AWS_SECRET_ACCESS_KEY"),
		AwsSession:        os.Getenv("AWS_SESSION_TOKEN"),
		AccountID:         accountId,
		XRayDaemonAddress: utils.GetEnvWithDefault("AWS_XRAY_DAEMON_ADDRESS", "127.0.0.1:2000"),
		FunctionName:      utils.GetEnvWithDefault("AWS_LAMBDA_FUNCTION_NAME", "test_function"),
		FunctionVersion:   functionVersion,

		// TODO: Implement runtime management controls
		// https://aws.amazon.com/blogs/compute/introducing-aws-lambda-runtime-management-controls/
		RuntimeInfo: interop.RuntimeInfo{
			ImageJSON: "{}",
			Arn:       "",
			Version:   ""},
		CustomerEnvironmentVariables: additionalFunctionEnvironmentVariables,
		SandboxType:                  interop.SandboxClassic,
		Bootstrap:                    bs,
		EnvironmentVariables:         env.NewEnvironment(),
	}, timeout*1000)
	initEnd := time.Now()
	return initStart, initEnd
}
