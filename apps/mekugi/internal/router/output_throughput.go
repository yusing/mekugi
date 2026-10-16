package router

import (
	"fmt"

	"github.com/yusing/mekugi/contracts/inference"
)

func outputThroughputLabel(throughput inference.OutputThroughput) string {
	if rate, known := throughput.Rate(); known {
		return fmt.Sprintf("%.1f tok/s", rate)
	}
	return ""
}
