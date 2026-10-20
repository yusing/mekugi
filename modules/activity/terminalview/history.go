package terminalview

import "cmp"

// HistoryAnchor freezes the visible source row across history insertion.
type HistoryAnchor struct {
	source      uint64
	row, offset int
}

// AnchorHistory lays out the current publication without advancing animation.
// Any retained-output demands are resolved through the ordinary hydration path.
// Source: apps/mekugi/internal/router/app_server_history_pages.go:398:411@eaad597b.
func (v *View) AnchorHistory(before uint64) HistoryAnchor {
	anchor := HistoryAnchor{row: -1, offset: v.Offset}
	if v.Following || v.Width <= 0 || v.FeedRows <= 0 {
		return anchor
	}
	layoutOnly := v.Painter.LayoutOnly
	v.Painter.LayoutOnly = true
	v.LayoutFeed(v.Width)
	v.Painter.LayoutOnly = layoutOnly
	for seq, row := range v.QuestionRows {
		if seq <= before && row <= v.Offset && row > anchor.row {
			anchor.source, anchor.row = seq, row
		}
	}
	return anchor
}

// HistoryInserted applies the replacement publication while retaining a paused
// viewport. Repeated calls after output hydration use the same frozen offset.
// Source: apps/mekugi/internal/router/app_server_history_pages.go:435:448@eaad597b.
func (v *View) HistoryInserted(anchor HistoryAnchor) {
	v.Runs = nil
	v.HistoryOrder = true
	clear(v.Events)
	for _, entry := range v.entries {
		if entry.Standalone() {
			if v.Events == nil {
				v.Events = make(map[string]Event)
			}
			v.Events[entry.Agent] = Event{entry.Seq, cmp.Or(entry.Observed, v.Now())}
		}
	}
	if anchor.source != 0 {
		layoutOnly := v.Painter.LayoutOnly
		v.Painter.LayoutOnly = true
		v.LayoutFeed(v.Width)
		v.Painter.LayoutOnly = layoutOnly
		if row, ok := v.QuestionRows[anchor.source]; ok {
			v.Offset = anchor.offset + row - anchor.row
		}
	}
}
