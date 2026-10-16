package terminalview

// Source: apps/mekugi/internal/router/live_activity_expansion.go:5:70@[29de53cc].

// Span is a cached run's position in transcript row coordinates.
type Span struct {
	Seq        uint64
	Start, End int
}

func (v *View) ToggleExpansion() {
	if !v.Following {
		for _, span := range v.FeedSpans {
			if span.End > v.Offset {
				v.ExpansionAnchor = &Span{Seq: span.Seq, Start: max(0, v.Offset-span.Start)}
				break
			}
		}
	}
	v.Expansion = (v.Expansion + 1) % 3
	// Hover targets describe the old geometry, not the newly disclosed content.
	v.Snippet, v.QuestionHover, v.EditHover = Snippet{}, 0, 0
}

func (v *View) excerpt(automatic bool) bool {
	return v.Expansion == 0 && automatic
}

func (v *View) discloseBlock(block Block) Block {
	if v.Expansion == 0 {
		return block
	}
	if block.Kind == "summary" || v.Expansion == 2 && (block.Kind == "op" || block.Kind == "reads") {
		block.Collapsed = false
		block.Expanded = true
		block.SourceRows, block.TailRows = 0, 0
		if result, exists := v.output(block, OutputExpanded); exists && !result.View.Released {
			block.Tail, block.TailOmitted = result.View.Lines, result.View.Dropped
		}
	}
	return block
}

// Keep all disclosure layouts warm, but discard stale groups, widths, themes,
// and pointer states. Entry invalidation already removes every variant.
func (v *View) retainRuns(used map[RunKey]Run) {
	base := func(key RunKey) RunKey {
		key.Expansion, key.Clip, key.Tail, key.Excerpt = 0, 0, 0, false
		return key
	}
	var active map[RunKey]bool
	for key, run := range v.Runs {
		if key.Expansion == v.Expansion {
			continue
		}
		if active == nil {
			active = make(map[RunKey]bool, len(used))
			for key := range used {
				active[base(key)] = true
			}
		}
		if active[base(key)] {
			used[key] = run
		}
	}
	v.Runs = used
}
