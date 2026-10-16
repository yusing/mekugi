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

func (v *liveActivityView) retainedOutput(block activityui.Block) *activitymodel.Output {
	if block.Output == 0 {
		return nil
	}
	for _, entry := range v.entries {
		if entry.Seq != block.Source || entry.native == nil {
			continue
		}
		if block.Output == 1 {
			return entry.native.output
		}
		if index := block.Output - 2; index < len(entry.native.segments) {
			return entry.native.segments[index].output
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

// outputContext supplies only the retained prefix the feed can use for syntax.
// Collapsed values and structured change rows never need stream context.
func (v *liveActivityView) outputContext(block activityui.Block) activityui.Block {
	if block.Collapsed || len(block.Tail) == 0 || len(block.Changes) > 0 {
		return block
	}
	if output := v.retainedOutput(block); output != nil {
		context, limited := output.Context(activityui.OutputContextBytes)
		block.OutputSnapshot, block.OutputContextLimited = &context, limited
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
