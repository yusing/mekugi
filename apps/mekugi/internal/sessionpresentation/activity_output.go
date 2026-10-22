package sessionpresentation

import (
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/activity_output.go:1:50@45fbe279

func RetainedOutputID(output *activitymodel.Output, id int) int {
	if output == nil {
		return 0
	}
	return id
}

func (v *ActivitySource) RetainedOutput(source uint64, output int) *activitymodel.Output {
	if output <= 0 {
		return nil
	}
	for _, entry := range v.Entries {
		if entry.Source.Seq != source || entry.Source.Native == nil {
			continue
		}
		if output == 1 {
			return entry.Source.Native.Output
		}
		if index := output - 2; index < len(entry.Source.Native.Segments) {
			return entry.Source.Native.Segments[index].Output
		}
	}
	return nil
}

// ResolveOutputDemands reads admitted source handles without accessing a renderer.
// Application integration delivers the detached results to the requesting view.
func (v *ActivitySource) ResolveOutputDemands(demands []activitypresentation.OutputDemand) []activitypresentation.OutputResult {
	results := make([]activitypresentation.OutputResult, 0, len(demands))
	for _, demand := range demands {
		result := activitypresentation.OutputResult{Demand: demand}
		output := v.RetainedOutput(demand.Source, demand.Output)
		if output == nil {
			result.View.Released = true
		} else if demand.Bytes == 0 {
			result.View = output.View()
		} else {
			result.View, result.Limited = output.Context(demand.Bytes)
		}
		results = append(results, result)
	}
	return results
}
