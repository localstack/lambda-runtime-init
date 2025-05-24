package server

import (
	"fmt"
	"io"
)

// TODO: Replace with interop.ReportMetrics when Memory stats are actually being collected

type InvokeReport struct {
	InvokeId         string
	DurationMs       float64
	BilledDurationMs float64
	// TODO: Make this a unint and properly collect it with a supervisor
	MemorySizeMB    string
	MaxMemoryUsedMB string
}

func (r *InvokeReport) Print(w io.Writer) error {
	_, err := fmt.Fprintf(w,
		"REPORT RequestId: %s\t"+
			"Duration: %.2f ms\t"+
			"Billed Duration: %.f ms\t"+
			"Memory Size: %s MB\t"+
			"Max Memory Used: %s MB\t\n",
		r.InvokeId, r.DurationMs, r.BilledDurationMs, r.MemorySizeMB, r.MaxMemoryUsedMB)

	return err
}
