package router

import (
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func retainedOutputID(output *activitymodel.Output, id int) int {
	if output == nil {
		return 0
	}
	return id
}

func (v *activityAdapter) retainedOutput(block activityui.Block) *activitymodel.Output {
	if block.Output == 0 {
		return nil
	}
	for _, entry := range v.Entries {
		if entry.Source.Seq != block.Source || entry.Source.native == nil {
			continue
		}
		if block.Output == 1 {
			return entry.Source.native.output
		}
		if index := block.Output - 2; index < len(entry.Source.native.segments) {
			return entry.Source.native.segments[index].output
		}
	}
	return nil
}
