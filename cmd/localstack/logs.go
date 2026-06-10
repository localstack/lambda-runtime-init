package main

import (
	"strings"
	"sync"

	"github.com/aws/aws-lambda-runtime-interface-emulator/internal/lsapi"
)

type LogCollector struct {
	mutex       *sync.Mutex
	RuntimeLogs []string
}

func (lc *LogCollector) Write(p []byte) (n int, err error) {
	lc.Put(string(p))
	return len(p), nil
}

func NewLogCollector() *LogCollector {
	return &LogCollector{
		RuntimeLogs: []string{},
		mutex:       &sync.Mutex{},
	}
}
func (lc *LogCollector) Put(line string) {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	lc.RuntimeLogs = append(lc.RuntimeLogs, line)
}

func (lc *LogCollector) reset() {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	lc.RuntimeLogs = []string{}
}

func (lc *LogCollector) getLogs() lsapi.LogResponse {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	// Emit the captured runtime output verbatim. Do NOT rewrite bare carriage returns to line
	// feeds: AWS keeps a bare CR inside a single CloudWatch log event (it splits records on LF
	// only), so a user `print("a\rb")` must stay the one event "a\rb". LocalStack's log ingestion
	// likewise splits on "\n" (see services/lambda_/.../logs.py), so converting CR to LF here
	// would wrongly split such records — see TestCloudwatchLogs::test_multi_line_prints.
	response := lsapi.LogResponse{
		Logs: strings.Join(lc.RuntimeLogs, ""),
	}
	lc.RuntimeLogs = []string{}
	return response
}
