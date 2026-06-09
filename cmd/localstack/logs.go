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
	logs := strings.Join(lc.RuntimeLogs, "")
	// The runtime emits multi-line records (e.g. an unhandled-init traceback) as a single log
	// frame with internal newlines replaced by bare carriage returns. AWS renders those back as
	// line feeds, so convert bare CR to LF while preserving genuine CRLF line endings (which AWS
	// keeps verbatim, e.g. the LAMBDA_WARNING line).
	const crlfPlaceholder = "\x00"
	logs = strings.ReplaceAll(logs, "\r\n", crlfPlaceholder)
	logs = strings.ReplaceAll(logs, "\r", "\n")
	logs = strings.ReplaceAll(logs, crlfPlaceholder, "\r\n")
	response := lsapi.LogResponse{
		Logs: logs,
	}
	lc.RuntimeLogs = []string{}
	return response
}
