package router

import (
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func retainedOutputID(output *activitymodel.Output, id int) int {
	if output == nil {
		return 0
	}
	return id
}

func (v *activityAdapter) retainedOutput(source uint64, output int) *activitymodel.Output {
	if output == 0 {
		return nil
	}
	for _, entry := range v.Entries {
		if entry.Source.Seq != source || entry.Source.native == nil {
			continue
		}
		if output == 1 {
			return entry.Source.native.output
		}
		if index := output - 2; index < len(entry.Source.native.segments) {
			return entry.Source.native.segments[index].output
		}
	}
	return nil
}

// resolveOutputDemands reads admitted source handles without accessing a renderer.
// Application integration delivers the detached results to the requesting view.
func (v *activityAdapter) resolveOutputDemands(demands []activitypresentation.OutputDemand) []activitypresentation.OutputResult {
	results := make([]activitypresentation.OutputResult, 0, len(demands))
	for _, demand := range demands {
		result := activitypresentation.OutputResult{Demand: demand}
		output := v.retainedOutput(demand.Source, demand.Output)
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
