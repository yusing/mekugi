package router

import (
	"cmp"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

const appServerHistoryPageSize = 100
const appServerHistoryMetadataPending = -2

// History belongs to this UI's stable root and child identities, not a router
// connection. It is rebuilt on resume; neither cursors nor processes persist.
type appServerChildHistory struct {
	root       string
	info       appServerThreadInfo
	turn       int
	anchor     string
	nextCursor string
	turnPages  int
	seenTurns  map[string]bool
	seenItems  map[[2]string]bool
	cursors    map[[2]string]bool
	itemAt     map[string]time.Time
	placements []*restoredPlacement
	initial    bool
	older      bool
	live       bool   // Live observations superseded this metadata snapshot, even after completion.
	firstSeq   uint64 // First retained entry of the previously loaded page, including rollout slots.
	failure    string
	requestID  string
	method     string
	params     map[string]any
	pending    []appserver.Message
}

func (u *appServerUI) startChildHistory(info appServerThreadInfo) error {
	r := u.restoring
	h := &appServerChildHistory{root: u.thread, info: info, turn: appServerHistoryMetadataPending, initial: true,
		seenTurns: make(map[string]bool), seenItems: make(map[[2]string]bool), cursors: make(map[[2]string]bool),
		itemAt: r.itemAt[info.ID], placements: r.pane[u.session.path(info.ID)]}
	if h.itemAt == nil {
		h.itemAt = make(map[string]time.Time)
	}
	delete(r.pane, u.session.path(info.ID))
	h.info.Turns = nil
	if u.childHistory == nil {
		u.childHistory = make(map[string]*appServerChildHistory)
	}
	u.childHistory[info.ID], u.historyLoading = h, h
	return u.requestChildHistory("thread/turns/list", map[string]any{"threadId": info.ID, "itemsView": "notLoaded", "sortDirection": "desc", "limit": appServerHistoryPageSize})
}

func (u *appServerUI) requestChildHistory(method string, params map[string]any) error {
	h := u.historyLoading
	h.method, h.params, h.failure = method, params, ""
	id, err := u.client.Send(method, params, true)
	if err != nil {
		return err
	}
	h.requestID = id
	u.requests[id] = "activity/" + method
	if h.initial {
		if method == "thread/turns/list" {
			u.status = fmt.Sprintf("Restoring Activity %d/%d · turn page %d…", u.restoring.next+1, len(u.restoring.order), h.turnPages+1)
		} else {
			u.status = fmt.Sprintf("Restoring Activity %d/%d · recent items…", u.restoring.next+1, len(u.restoring.order))
		}
	}
	u.updateHistoryHint()
	return nil
}

func (u *appServerUI) childHistoryResponse(method string, m appserver.Message) error {
	h := u.historyLoading
	if h == nil || h.root != u.thread || h.requestID != string(m.ID) || u.childHistory[h.info.ID] != h {
		return nil
	}
	h.requestID = ""
	if m.Error != nil {
		return u.failChildHistory(m.Error.Message)
	}
	if method == "activity/thread/turns/list" {
		var page struct {
			Data       []appServerHistoryTurn `json:"data"`
			NextCursor string                 `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &page); err != nil {
			return u.failChildHistory("Invalid turn page: " + err.Error())
		}
		for _, turn := range page.Data {
			if turn.ID == "" {
				return u.failChildHistory("Turn page returned no turn identity.")
			}
		}
		for _, turn := range page.Data {
			if h.seenTurns[turn.ID] {
				continue
			}
			h.seenTurns[turn.ID] = true
			turn.Items = nil // Metadata never masquerades as a complete item history.
			h.info.Turns = append(h.info.Turns, turn)
		}
		h.turnPages++
		if page.NextCursor != "" {
			key := [2]string{"turns", page.NextCursor}
			if h.cursors[key] {
				return u.failChildHistory("Turn history returned a repeated pagination cursor.")
			}
			h.cursors[key] = true
			if h.turnPages < 8 {
				params := map[string]any{"threadId": h.info.ID, "itemsView": "notLoaded", "sortDirection": "desc", "limit": appServerHistoryPageSize, "cursor": page.NextCursor}
				return u.requestChildHistory("thread/turns/list", params)
			}
			u.restoreContentNotice("Activity turn metadata is limited to eight pages; older turns and their evidence were not loaded.")
		}
		slices.Reverse(h.info.Turns)
		h.turn = len(h.info.Turns) - 1
		u.hydrateChildHistoryMetadata(h)
		h.older = false
		if h.turn < 0 {
			return u.finishChildHistory()
		}
		return u.requestChildItems(nil)
	}
	var page struct {
		Data []struct {
			TurnID        string        `json:"turnId"`
			Item          appServerItem `json:"item"`
			StartedAtMs   int64         `json:"startedAtMs"`
			CompletedAtMs int64         `json:"completedAtMs"`
		} `json:"data"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(m.Result, &page); err != nil {
		return u.failChildHistory("Invalid item page: " + err.Error())
	}
	turn := h.info.Turns[h.turn]
	for _, entry := range page.Data {
		if entry.TurnID != turn.ID || entry.Item.ID == "" {
			return u.failChildHistory("Item page returned an out-of-scope item identity.")
		}
	}
	if page.NextCursor != "" {
		key := [2]string{turn.ID, page.NextCursor}
		if h.cursors[key] {
			return u.failChildHistory("Item history returned a repeated pagination cursor.")
		}
		h.cursors[key] = true
	}
	var items []appServerItem
	for _, entry := range page.Data {
		key := [2]string{turn.ID, entry.Item.ID}
		if h.seenItems[key] {
			continue
		}
		h.seenItems[key] = true
		if at := cmp.Or(entry.CompletedAtMs, entry.StartedAtMs); at != 0 {
			h.itemAt[entry.Item.ID] = time.UnixMilli(at)
		}
		items = append(items, entry.Item)
	}
	// Source: codex-rs/app-server-protocol/src/protocol/v2/thread.rs@de9e78e3e
	// ThreadItemsListAnchor is exclusive and turn-scoped. Pages arrive newest
	// first; presentation and rollout slots remain chronological.
	slices.Reverse(items)
	if len(page.Data) > 0 {
		h.anchor = page.Data[len(page.Data)-1].Item.ID
	}
	h.nextCursor = page.NextCursor
	turn.Items, turn.olderPage = items, h.older
	u.renderChildHistoryPage(h, turn, page.NextCursor == "")
	if page.NextCursor == "" {
		h.turn--
		h.anchor = ""
	}
	return u.finishChildHistory()
}

func (u *appServerUI) requestChildItems(cursor any) error {
	h := u.historyLoading
	params := map[string]any{"threadId": h.info.ID, "turnId": h.info.Turns[h.turn].ID, "sortDirection": "desc", "limit": appServerHistoryPageSize}
	if cursor != nil {
		params["cursor"] = cursor
	}
	return u.requestChildHistory("thread/items/list", params)
}

func (u *appServerUI) failChildHistory(message string) error {
	h := u.historyLoading
	h.failure = message
	if h.initial {
		u.restoreHistoryNotice("Could not restore "+u.session.path(h.info.ID)+" history: "+message, false)
		u.restoring.pane[u.session.path(h.info.ID)] = h.placements
	} else {
		u.setNotice("Older Activity history: "+message+" · o retries", true)
	}
	return u.finishChildHistory()
}

func (u *appServerUI) finishChildHistory() error {
	h := u.historyLoading
	u.historyLoading = nil
	if h.initial {
		h.initial = false
		r := u.restoring
		r.next++
		return u.readNextRestoredChild()
	}
	u.updateHistoryHint()
	pending := h.pending
	h.pending = nil
	for _, m := range pending {
		if err := u.message(m); err != nil {
			return err
		}
	}
	return nil
}

func (u *appServerUI) historyTarget() *appServerChildHistory {
	for _, h := range u.childHistory {
		if h.root == u.thread && u.session.path(h.info.ID) == u.agents.selected && (h.turn != -1 || h.failure != "") {
			return h
		}
	}
	for _, agent := range u.session.agents {
		if u.agents.only && agent.Name != u.agents.selected {
			continue
		}
		for _, h := range u.childHistory {
			if h.root == u.thread && u.session.path(h.info.ID) == agent.Name && (h.turn != -1 || h.failure != "") {
				return h
			}
		}
	}
	return nil
}

func (u *appServerUI) updateHistoryHint() {
	if u.agents == nil {
		return
	}
	u.agents.historyHint = ""
	if h := u.historyLoading; h != nil && !h.initial {
		u.agents.historyHint = "Loading older · Esc cancels"
	} else if h := u.historyTarget(); h != nil {
		u.agents.historyHint = "Older history · o load"
		if h.failure != "" {
			u.agents.historyHint = "History unavailable · o retry"
		}
	}
}

func (u *appServerUI) loadOlderActivity() error {
	if u.restoring != nil || u.historyLoading != nil {
		return nil
	}
	h := u.historyTarget()
	if h == nil {
		return nil
	}
	u.historyLoading, h.older = h, true
	if h.failure != "" || h.turn == appServerHistoryMetadataPending {
		h.older = h.anchor != ""
		return u.requestChildHistory(h.method, h.params)
	}
	if h.nextCursor != "" {
		if h.anchor != "" {
			return u.requestChildItems(map[string]any{"type": "item", "itemId": h.anchor})
		}
		return u.requestChildItems(h.nextCursor)
	}
	// Moving to the next older turn starts a new page without an anchor.
	h.older = false
	return u.requestChildItems(nil)
}

func (u *appServerUI) cancelOlderActivity() error {
	h := u.historyLoading
	if h == nil || h.initial {
		return nil
	}
	delete(u.requests, h.requestID) // Cancel observation, not any host execution.
	h.requestID = ""
	u.setNotice("Older Activity loading cancelled", false)
	return u.finishChildHistory()
}

func (u *appServerUI) holdOlderHistoryEvent(m appserver.Message) (bool, error) {
	h := u.historyLoading
	if h == nil || h.initial || m.Method == "" {
		return false, nil
	}
	var params struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(m.Params, &params)
	if params.ThreadID != h.info.ID {
		return false, nil
	}
	if strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta") {
		return true, nil // Completed items own full content, as during resume.
	}
	if len(h.pending) == 256 {
		// The live event wins; discard the outstanding historical observation.
		if err := u.cancelOlderActivity(); err != nil {
			return true, err
		}
		return false, nil
	}
	h.pending = append(h.pending, m)
	return true, nil
}

func (u *appServerUI) renderChildHistoryPage(h *appServerChildHistory, turn appServerHistoryTurn, exhausted bool) {
	var selected, remaining []*restoredPlacement
	for _, p := range h.placements {
		anchored := p.turn == turn.ID && (slices.ContainsFunc(turn.Items, func(item appServerItem) bool { return item.ID == p.anchor }) || exhausted)
		if p.turn == "" && restoredTurnFor(h.info.Turns, h.itemAt, p.at) == h.turn {
			oldest := historyTime(turn.StartedAt)
			if len(turn.Items) > 0 {
				oldest = cmp.Or(h.itemAt[turn.Items[0].ID], oldest)
			}
			anchored = exhausted || !oldest.IsZero() && !p.at.Before(oldest)
		}
		if anchored {
			selected = append(selected, p)
		} else {
			remaining = append(remaining, p)
		}
	}
	h.placements = remaining
	name := u.session.path(h.info.ID)
	r := u.restoring
	if r == nil {
		r = &appServerActivityRestore{pane: make(map[string][]*restoredPlacement), itemAt: make(map[string]map[string]time.Time)}
		u.restoring = r
		defer func() { u.restoring = nil }()
	}
	wasPaging := r.paging
	r.paging = true
	defer func() { r.paging = wasPaging }()
	r.pane[name], r.itemAt[h.info.ID] = selected, h.itemAt
	before := u.agents.lastSeq
	info := h.info
	info.Turns = []appServerHistoryTurn{turn}
	// Historical pages may not reset a newer live lifecycle or cumulative
	// counters. Initial metadata supplies the complete turn count and timing.
	agent := *u.session.agent(name)
	u.restoreActivityThread(info)
	*u.session.agent(name) = agent
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
	first := uint64(0)
	for _, entry := range u.agents.entries {
		if entry.Seq > before {
			first = entry.Seq
			break
		}
	}
	if !u.historyLoading.initial {
		retained := slices.ContainsFunc(u.agents.entries, func(entry liveActivityRecord) bool { return entry.Seq == h.firstSeq })
		u.agents.insertHistory(before, func(entry activityPaneEntry) bool {
			return retained && entry.Seq == h.firstSeq || !retained && entry.native != nil && entry.native.thread == h.info.ID
		})
		for _, p := range r.main {
			// A failed initial read may already have placed this retained
			// assignment/message in Main without its Activity link. The same
			// source observation keeps its identity through the pending slots.
			existing := slices.IndexFunc(u.view.entries, func(entry liveActivityRecord) bool {
				return p.entry.assignment != nil && entry.assignment == p.entry.assignment || p.entry.message != nil && entry.message == p.entry.message
			})
			if existing >= 0 {
				if p.link != nil && mainActivityLinked(p.entry) {
					entry := u.view.entries[existing].activityPaneEntry
					entry.activitySeq = *p.link
					u.view.replaceEntry(existing, entry, parseLiveActivity(entry))
				}
				continue
			}
			before := u.view.lastSeq
			u.applyRestoredMain([]*restoredPlacement{p})
			u.view.insertHistory(before, func(entry activityPaneEntry) bool { return entry.Observed.After(p.at) })
		}
	}
	if first != 0 {
		h.firstSeq = first
	}
	u.agents.relinkHistoryAnswers(name)
	u.view.relinkHistoryAnswers(name)
}

func (u *appServerUI) hydrateChildHistoryMetadata(h *appServerChildHistory) {
	agent := u.session.agent(u.session.path(h.info.ID))
	if !h.live {
		agent.Started, agent.LastResponse = restoredAgentTimes(h.info)
		agent.WorkTimer = restoredAgentWork(h.info)
		agent.Turns = max(agent.Turns, uint64(len(h.info.Turns)))
		if len(h.info.Turns) > 0 {
			agent.Final = h.info.Turns[len(h.info.Turns)-1].Status == "completed"
		}
	}
	restoreContextUsage(agent, h.info)
	u.restoreUsage(h.info)
	u.observeCost(h.info.ID, agent)
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
}

// Cumulative final snapshots are presentation copies. Rebuild the affected
// child's answers from retained source text only after chronological insertion,
// so later snapshots update earlier answers and late assignments acquire links.
func (v *liveActivityView) relinkHistoryAnswers(owner string) {
	for i, entry := range v.entries {
		if entry.Agent == owner && entry.Kind == "final" {
			v.replaceEntry(i, entry.activityPaneEntry, parseLiveActivity(entry.activityPaneEntry))
		}
	}
	for _, entry := range v.entries {
		if entry.Agent == owner && entry.Kind == "final" {
			v.linkChildAnswers(entry.Seq)
		}
	}
}

// Move the newly rendered page before the child's previously loaded entries.
// Sequence IDs remain stable for output and Main-to-Activity links. Parsing,
// deduplication, output ownership and retention stay with the existing view.
func (v *liveActivityView) insertHistory(before uint64, belongs func(activityPaneEntry) bool) {
	var anchor uint64
	anchorRow := -1
	if !v.following && v.width > 0 && v.feedRows > 0 {
		layoutOnly := v.painter.LayoutOnly
		v.painter.LayoutOnly = true
		v.layoutFeed(v.width, v.feedRows)
		v.painter.LayoutOnly = layoutOnly
		for seq, row := range v.questionRows {
			if seq <= before && row <= v.offset && row > anchorRow {
				anchor, anchorRow = seq, row
			}
		}
	}
	var indices []int
	for i, entry := range v.entries {
		if entry.Seq <= before {
			indices = append(indices, i)
		}
	}
	at := slices.IndexFunc(indices, func(i int) bool { return belongs(v.entries[i].activityPaneEntry) })
	if at < 0 {
		at = len(indices)
	}
	var added []int
	for i, entry := range v.entries {
		if entry.Seq > before {
			added = append(added, i)
		}
	}
	indices = slices.Insert(indices, at, added...)
	v.reorderEntries(indices)
	v.historyOrder = true
	clear(v.events)
	for _, entry := range v.entries {
		if v.standalone(entry.activityPaneEntry) {
			if v.events == nil {
				v.events = make(map[string]liveActivityEvent)
			}
			v.events[entry.Agent] = liveActivityEvent{entry.Seq, cmp.Or(entry.Observed, v.now())}
		}
	}
	if anchor != 0 {
		layoutOnly := v.painter.LayoutOnly
		v.painter.LayoutOnly = true
		v.layoutFeed(v.width, v.feedRows)
		v.painter.LayoutOnly = layoutOnly
		if row, ok := v.questionRows[anchor]; ok {
			v.offset += row - anchorRow
		}
	}
}
