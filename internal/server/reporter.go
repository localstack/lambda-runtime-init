package server

import (
	"fmt"
	"io"
	"math"
	"time"
)

// TODO: This should be creating a Report struct which, in turn, should be Stringified into the "REPORT RequestId: ..." syntax

func PrintEndReports(invokeId string, initDuration string, memorySize string, invokeStart time.Time, timeoutDuration time.Duration, w io.Writer) {
	// Calculate invoke duration
	invokeDuration := math.Min(float64(time.Now().Sub(invokeStart).Nanoseconds()),
		float64(timeoutDuration.Nanoseconds())) / float64(time.Millisecond)

	_, _ = fmt.Fprintln(w, "END RequestId: "+invokeId)
	// We set the Max Memory Used and Memory Size to be the same (whatever it is set to) since there is
	// not a clean way to get this information from rapidcore
	_, _ = fmt.Fprintf(w,
		"REPORT RequestId: %s\t"+
			initDuration+
			"Duration: %.2f ms\t"+
			"Billed Duration: %.f ms\t"+
			"Memory Size: %s MB\t"+
			"Max Memory Used: %s MB\t\n",
		invokeId, invokeDuration, math.Ceil(invokeDuration), memorySize, memorySize)
}
