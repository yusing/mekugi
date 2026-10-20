package sessionpresentation

import (
	"errors"
	"slices"
)

const HistoryPageSize = 100

type PageKind uint8

const (
	NoPage PageKind = iota
	TurnMetadata
	TurnItems
)

// PageRequest contains semantic cursor choices, not backend parameter shapes.
type PageRequest struct {
	Kind   PageKind
	Thread string
	Turn   string
	Cursor string
	Anchor string
}

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

// History stores pagination identities, never transcript or host payloads.
// Source: apps/mekugi/internal/router/app_server_history_pages.go:14:329@db279442
type History struct {
	thread     string
	turns      []string
	turn       int
	anchor     string
	nextCursor string
	turnPages  int
	seenTurns  map[string]bool
	seenItems  map[[2]string]bool
	cursors    map[[2]string]bool
	initial    bool
	older      bool
	live       bool
	failure    string
	requestID  string
	last       PageRequest
}

func (h *History) View(loading string) HistoryView {
	v := HistoryView{Thread: h.thread, Failure: h.failure, Initial: h.initial, TurnPages: h.turnPages, Live: h.live}
	switch {
	case loading == h.thread && h.thread != "":
		v.Status = Loading
	case h.failure != "":
		v.Status = Failed
	case h.Available():
		v.Status = Unloaded
	}
	return v
}

func (h *History) Available() bool       { return h.thread != "" && (h.turn != -1 || h.failure != "") }
func (h *History) ObserveLive()          { h.live = true }
func (h *History) BindRequest(id string) { h.requestID = id }
func (h *History) RequestID() string     { return h.requestID }
func (h *History) AcceptResponse(id string) bool {
	if id == "" || id != h.requestID {
		return false
	}
	h.requestID = ""
	return true
}
func (h *History) Fail(message string) { h.failure = message }

func (h *History) request(kind PageKind, turn, cursor, anchor string) PageRequest {
	h.failure = ""
	h.last = PageRequest{Kind: kind, Thread: h.thread, Turn: turn, Cursor: cursor, Anchor: anchor}
	return h.last
}

func (h *History) next() PageRequest {
	h.older = true
	if h.failure != "" || h.turn == -2 {
		h.older = h.anchor != ""
		last := h.last
		return h.request(last.Kind, last.Turn, last.Cursor, last.Anchor)
	}
	if h.nextCursor != "" {
		if h.anchor != "" {
			return h.request(TurnItems, h.turns[h.turn], "", h.anchor)
		}
		return h.request(TurnItems, h.turns[h.turn], h.nextCursor, "")
	}
	h.older = false
	return h.request(TurnItems, h.turns[h.turn], "", "")
}

type TurnPage struct {
	Accepted []int // Positions in the supplied page, in arrival order.
	Complete bool
	Limited  bool
	Next     PageRequest
}

func (h *History) Turns(ids []string, cursor string) (TurnPage, error) {
	var page TurnPage
	for _, id := range ids {
		if id == "" {
			return page, errors.New("Turn page returned no turn identity.")
		}
	}
	if h.seenTurns == nil {
		h.seenTurns = make(map[string]bool)
	}
	for i, id := range ids {
		if !h.seenTurns[id] {
			h.seenTurns[id] = true
			h.turns = append(h.turns, id)
			page.Accepted = append(page.Accepted, i)
		}
	}
	h.turnPages++
	if cursor != "" {
		if !h.cursor("turns", cursor) {
			return page, errors.New("Turn history returned a repeated pagination cursor.")
		}
		if h.turnPages < 8 {
			page.Next = h.request(TurnMetadata, "", cursor, "")
			return page, nil
		}
		page.Limited = true
	}
	slices.Reverse(h.turns)
	h.turn, h.older = len(h.turns)-1, false
	page.Complete = true
	if h.turn >= 0 {
		page.Next = h.request(TurnItems, h.turns[h.turn], "", "")
	}
	return page, nil
}

type ItemIdentity struct{ Turn, Item string }

type ItemPage struct {
	Accepted  []int // Positions in chronological order for insertion into Activity.
	Turn      int
	Older     bool
	Exhausted bool
}

func (h *History) Items(ids []ItemIdentity, cursor string) (ItemPage, error) {
	page := ItemPage{Turn: h.turn, Older: h.older, Exhausted: cursor == ""}
	if h.turn < 0 || h.turn >= len(h.turns) {
		return page, errors.New("Item page returned outside an active turn.")
	}
	turn := h.turns[h.turn]
	for _, id := range ids {
		if id.Turn != turn || id.Item == "" {
			return page, errors.New("Item page returned an out-of-scope item identity.")
		}
	}
	if cursor != "" && !h.cursor(turn, cursor) {
		return page, errors.New("Item history returned a repeated pagination cursor.")
	}
	if h.seenItems == nil {
		h.seenItems = make(map[[2]string]bool)
	}
	for i, id := range ids {
		key := [2]string{turn, id.Item}
		if !h.seenItems[key] {
			h.seenItems[key] = true
			page.Accepted = append(page.Accepted, i)
		}
	}
	slices.Reverse(page.Accepted)
	if len(ids) > 0 {
		h.anchor = ids[len(ids)-1].Item
	}
	h.nextCursor = cursor
	if cursor == "" {
		h.turn--
		h.anchor = ""
	}
	return page, nil
}

func (h *History) cursor(turn, cursor string) bool {
	if h.cursors == nil {
		h.cursors = make(map[[2]string]bool)
	}
	key := [2]string{turn, cursor}
	if h.cursors[key] {
		return false
	}
	h.cursors[key] = true
	return true
}

type BufferDecision uint8

const (
	Deliver BufferDecision = iota
	DiscardDelta
	Buffer
	CancelRead
)

func (h *History) BufferEvent(thread string, delta bool, pending int) BufferDecision {
	if h.initial || thread != h.thread {
		return Deliver
	}
	if delta {
		return DiscardDelta
	}
	if pending >= 256 {
		return CancelRead
	}
	return Buffer
}
