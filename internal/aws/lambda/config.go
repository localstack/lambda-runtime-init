package lambda

type FunctionConfig struct {
	FunctionName         string // AWS_LAMBDA_FUNCTION_NAME
	FunctionMemorySizeMb string // AWS_LAMBDA_FUNCTION_MEMORY_SIZE
	FunctionVersion      string // AWS_LAMBDA_FUNCTION_VERSION
	FunctionTimeoutSec   string // AWS_LAMBDA_FUNCTION_TIMEOUT
	InitializationType   string // AWS_LAMBDA_INITIALIZATION_TYPE
	LogGroupName         string // AWS_LAMBDA_LOG_GROUP_NAME
	LogStreamName        string // AWS_LAMBDA_LOG_STREAM_NAME
	FunctionHandler      string //  AWS_LAMBDA_FUNCTION_HANDLER || _HANDLER
}
