// Source: capturer/ax.go@fdb889f8.
package evidence

import (
	"strings"
)

// AXEditMetrics reports measured emissions separately from defect judgments.
type AXEditMetrics struct {
	Calls              uint64 `json:"calls"`
	EmittedBytes       uint64 `json:"emitted_bytes"`
	ReEmittedLineBytes uint64 `json:"re_emitted_line_bytes"`
	Failed             uint64 `json:"failed"`
	Unconfirmed        uint64 `json:"unconfirmed"`
}

// AXEditAccumulator holds only the preceding original payload for exact,
// line-aligned repetition measurement, never a durable script history.
type AXEditAccumulator struct {
	Metrics  AXEditMetrics
	previous string
}

func (accumulator *AXEditAccumulator) Observe(script string, failed, unconfirmed bool) {
	accumulator.Metrics.Calls++
	accumulator.Metrics.EmittedBytes += uint64(len(script))
	if failed {
		accumulator.Metrics.Failed++
	}
	if unconfirmed {
		accumulator.Metrics.Unconfirmed++
	}
	previous := make(map[string]int)
	for line := range strings.Lines(accumulator.previous) {
		previous[line]++
	}
	for line := range strings.Lines(script) {
		if previous[line] > 0 {
			accumulator.Metrics.ReEmittedLineBytes += uint64(len(line))
			previous[line]--
		}
	}
	accumulator.previous = script
}
