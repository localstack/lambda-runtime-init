package logging

import (
	"io"
	"os"
)

// This LocalStack LogsEgressAPI builder allows to customize log capturing, in our case using the logCollector.

type LocalStackLogsEgressAPI struct {
	logCollector *LogCollector
}

func NewLocalStackLogsEgressAPI(logCollector *LogCollector) *LocalStackLogsEgressAPI {
	return &LocalStackLogsEgressAPI{
		logCollector: logCollector,
	}
}

// The interface StdLogsEgressAPI for the functions below is defined in the under cmd/localstack/logs_egress_api.go
// The default implementation is a NoOpLogsEgressAPI

func (s *LocalStackLogsEgressAPI) GetExtensionSockets() (io.Writer, io.Writer, error) {
	// os.Stderr can not be used for the stderrWriter because stderr is for internal logging (not customer visible).
	return io.MultiWriter(s.logCollector, os.Stdout), io.MultiWriter(s.logCollector, os.Stdout), nil
}

func (s *LocalStackLogsEgressAPI) GetRuntimeSockets() (io.Writer, io.Writer, error) {
	// os.Stderr can not be used for the stderrWriter because stderr is for internal logging (not customer visible).
	return io.MultiWriter(s.logCollector, os.Stdout), io.MultiWriter(s.logCollector, os.Stdout), nil
}
