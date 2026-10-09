package router

import activityui "github.com/yusing/mekugi/internal/ui/activity"

// liveActivitySpan is a cached run's position in transcript row coordinates.
type liveActivitySpan struct {
	seq        uint64
	start, end int
}

func (v *liveActivityView) toggleExpansion() {
	if !v.following {
		for _, span := range v.feedSpans {
			if span.end > v.offset {
				v.expansionAnchor = &liveActivitySpan{seq: span.seq, start: max(0, v.offset-span.start)}
				break
			}
		}
	}
	v.expansion = (v.expansion + 1) % 3
	// Hover targets describe the old geometry, not the newly disclosed content.
	v.snippet, v.questionHover, v.editHover = liveActivitySnippet{}, 0, 0
}

func (v *liveActivityView) excerpt(automatic bool) bool {
	return v.expansion == 0 && automatic
}

func (v *liveActivityView) discloseBlock(block activityui.Block) activityui.Block {
	if v.expansion == 0 {
		return block
	}
	if block.Kind == "summary" || v.expansion == 2 && (block.Kind == "op" || block.Kind == "reads") {
		block.Collapsed = false
		block.Expanded = true
		block.SourceRows, block.TailRows = 0, 0
		if block.Output != nil {
			output := block.Output.View()
			if !output.Released {
				block.Tail, block.TailOmitted = output.Lines, output.Dropped
			}
		}
	}
	return block
}

// Keep all disclosure layouts warm, but discard stale groups, widths, themes,
// and pointer states. Entry invalidation already removes every variant.
func (v *liveActivityView) retainRuns(used map[liveActivityRunKey]liveActivityRun) {
	base := func(key liveActivityRunKey) liveActivityRunKey {
		key.expansion, key.clip, key.tail, key.excerpt = 0, 0, 0, false
		return key
	}
	active := make(map[liveActivityRunKey]bool, len(used))
	for key := range used {
		active[base(key)] = true
	}
	for key, run := range v.runs {
		if key.expansion != v.expansion && active[base(key)] {
			used[key] = run
		}
	}
	v.runs = used
}
