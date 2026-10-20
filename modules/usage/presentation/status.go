package presentation

import (
	"fmt"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/presentation"
)

var Throughput = presentation.Status{
	ID:         "usage.throughput",
	Placements: []presentation.Placement{presentation.ComposerCaption, presentation.AgentMetric},
	Text:       func(thread presentation.Thread) string { return OutputThroughputLabel(thread.OutputThroughput) },
}

// Source: modules/activity/terminalview/metrics.go:22:27@[96eacf0f]
func OutputThroughputLabel(throughput inference.OutputThroughput) string {
	if rate, known := throughput.Rate(); known {
		return fmt.Sprintf("%.1f tok/s", rate)
	}
	return ""
}
