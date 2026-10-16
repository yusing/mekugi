package router

import (
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// outputOrigin retains one original page and its direct attached output handle.
// It survives feed aging without a second registry or a parallel member tree.
type outputOrigin struct {
	block  activityui.Block
	output *activitymodel.Output
}

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

func snapshotOutput(block activityui.Block, output *activitymodel.Output) activityui.Block {
	if output != nil {
		snapshot := output.View()
		block.OutputSnapshot, block.OutputContextLimited = &snapshot, false
	}
	return block
}

func (d *outputDialog) retainedOutput(block activityui.Block) *activitymodel.Output {
	if output := d.view.retainedOutput(block); output != nil {
		return output
	}
	for _, origin := range d.origins {
		if origin.block.Source == block.Source && origin.block.Output == block.Output {
			return origin.output
		}
	}
	return nil
}

// replaceOrigins keeps exactly the current flat pages, retaining direct handles
// before their source records can age out. No member tree is duplicated here.
func (d *outputDialog) replaceOrigins(pages []activityui.Block) {
	origins := make([]outputOrigin, len(pages))
	for i, page := range pages {
		origins[i] = outputOrigin{page, d.retainedOutput(page)}
	}
	d.origins = origins
}
