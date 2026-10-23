package sessionpresentation

import (
	"slices"
)

type HistoryStatus uint8

const (
	Exhausted HistoryStatus = iota
	Unloaded
	Loading
	Failed
)

type HistoryView struct {
	Thread    string
	Status    HistoryStatus
	Failure   string
	Initial   bool
	TurnPages int
	Live      bool
}

// HistoryDock publishes admitted history in roster order, independently of the
// highlighted row. Target is the preferred eligible child, or the first one
// while Main is displayed. Loading retains its identity until settlement.
type HistoryDock struct {
	Entries []HistoryView
	Target  string
}

func (m *Model) HistoryView(entries []HistoryView) HistoryDock {
	v := HistoryDock{Entries: slices.Clone(entries)}
	if m.replay {
		return v
	}
	preferred := m.PreferredHistory()
	for _, h := range entries {
		if h.Status == Exhausted || h.Status == Loading {
			continue
		}
		if h.Thread == preferred {
			v.Target = h.Thread
			break
		}
		if v.Target == "" && m.view.Displayed.ID == m.view.Main.ID {
			v.Target = h.Thread
		}
	}
	return v
}
