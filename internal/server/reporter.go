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
	// TODO: Make this a uint and properly collect it with a supervisor
	MemorySizeMB    string
	MaxMemoryUsedMB string

	InitDurationMs float64
	Status         string
}

func (r *InvokeReport) Print(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "END RequestId: "+r.InvokeId); err != nil {
		return err
	}

	report := fmt.Sprintf("REPORT RequestId: %s\t"+
		"Duration: %.2f ms\t"+
		"Billed Duration: %.0f ms\t"+
		"Memory Size: %s MB\t"+
		"Max Memory Used: %s MB\t"+
		"Init Duration: %.2f ms",
		r.InvokeId, r.DurationMs, r.BilledDurationMs, r.MemorySizeMB, r.MaxMemoryUsedMB, r.InitDurationMs)

	if r.Status != "" {
		report += fmt.Sprintf("\tStatus: %s", r.Status)
	}

	if _, err := fmt.Fprintln(w, report); err != nil {
		return err
	}

	return nil
}

type InitReport struct {
	InvokeId   string
	DurationMs float64
	Status     string
}

func (r *InitReport) Print(w io.Writer) error {
	_, err := fmt.Fprintf(w,
		"INIT_REPORT Init Duration: %.2f ms Phase: init Status: %s",
		r.DurationMs,
		r.Status,
	)

	return err
}
