package logging

import (
	"strings"
	"sync"
)

type LogResponse struct {
	Logs string `json:"logs"`
}

type LogCollector struct {
	logs strings.Builder
	mu   *sync.Mutex
}

func NewLogCollector() *LogCollector {
	return &LogCollector{
		mu: &sync.Mutex{},
	}
}

func (lc *LogCollector) Write(p []byte) (n int, err error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	n, err = lc.logs.Write(p)
	return
}

func (lc *LogCollector) GetLogs() LogResponse {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	resp := LogResponse{Logs: lc.logs.String()}
	lc.logs.Reset()
	return resp
}
