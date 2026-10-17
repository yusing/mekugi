package router

import (
	"cmp"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Protocol payloads, deferred activity content and live event buffering stay in
// the adapter. Pagination identity and transitions live in session presentation.
type appServerChildHistory struct {
	state      *sessionpresentation.History
	info       binding.ThreadInfo
	itemAt     map[string]time.Time
	placements []*restoredPlacement
	firstSeq   uint64
	pending    []appserver.Message
	skills     *appServerSkillHistory
}

func (u *appServerUI) childHistory(thread string) *appServerChildHistory {
	if store := u.session.FindThread(thread); store != nil {
		return *u.childHistoryState.Value(store)
	}
	return nil
}

func (u *appServerUI) historyLoading() *appServerChildHistory {
	return u.childHistory(u.sessionPresentation().View().Loading)
}

func (u *appServerUI) startChildHistory(info binding.ThreadInfo) error {
	r := u.restoring
	store := u.session.Thread(info.ID)
	h := &appServerChildHistory{state: u.presentationState.History(store), info: info,
		itemAt: r.itemAt[info.ID], placements: r.pane[u.session.Caller(info.ID)]}
	if h.itemAt == nil {
		h.itemAt = make(map[string]time.Time)
	}
	delete(r.pane, u.session.Caller(info.ID))
	h.info.Turns = nil
	*u.childHistoryState.Value(store) = h
	request := u.sessionPresentation().StartHistory(h.state, info.ID)
	u.childSource.MarkSkillHistoryUnknown(u.session.Caller(info.ID))
	return u.requestChildHistory(request)
}

func (u *appServerUI) requestChildHistory(request sessionpresentation.PageRequest) error {
	h := u.historyLoading()
	method := "thread/items/list"
	params := map[string]any{"threadId": request.Thread, "sortDirection": "desc", "limit": sessionpresentation.HistoryPageSize}
	if request.Kind == sessionpresentation.TurnMetadata {
		method = "thread/turns/list"
		params["itemsView"] = "notLoaded"
	} else {
		params["turnId"] = request.Turn
	}
	if request.Anchor != "" {
		params["cursor"] = map[string]any{"type": "item", "itemId": request.Anchor}
	} else if request.Cursor != "" {
		params["cursor"] = request.Cursor
	}
	id, err := u.requests.Send(method, params, "main", appServerRequest{label: "activity/" + method})
	if err != nil {
		return err
	}
	h.state.BindRequest(id)
	state := h.state.View(request.Thread)
	if state.Initial {
		progress := u.restoring.state.View()
		if request.Kind == sessionpresentation.TurnMetadata {
			u.sessionPresentation().Status.SetText(fmt.Sprintf("Restoring Activity %d/%d · turn page %d…", progress.Next+1, len(progress.Threads), state.TurnPages+1))
		} else {
			u.sessionPresentation().Status.SetText(fmt.Sprintf("Restoring Activity %d/%d · recent items…", progress.Next+1, len(progress.Threads)))
		}
	}
	u.updateHistoryHint()
	return nil
}

func (u *appServerUI) childHistoryResponse(method string, m appserver.Message) error {
	h := u.historyLoading()
	if h == nil || !h.state.AcceptResponse(string(m.ID)) {
		return nil
	}
	if m.Error != nil {
		return u.failChildHistory(m.Error.Message)
	}
	if method == "activity/thread/turns/list" {
		var page binding.TurnPage
		if err := json.Unmarshal(m.Result, &page); err != nil {
			return u.failChildHistory("Invalid turn page: " + err.Error())
		}
		ids := make([]string, len(page.Data))
		for i, turn := range page.Data {
			ids[i] = turn.ID
		}
		decision, err := h.state.Turns(ids, page.NextCursor)
		for _, index := range decision.Accepted {
			turn := page.Data[index]
			turn.Items = nil // Metadata never masquerades as complete item history.
			h.info.Turns = append(h.info.Turns, turn)
		}
		if err != nil {
			return u.failChildHistory(err.Error())
		}
		if !decision.Complete {
			return u.requestChildHistory(decision.Next)
		}
		if decision.Limited {
			u.restoreContentNotice("Activity turn metadata is limited to eight pages; older turns and their evidence were not loaded.")
		}
		slices.Reverse(h.info.Turns)
		u.hydrateChildHistoryMetadata(h)
		if decision.Next.Kind == sessionpresentation.NoPage {
			// Thread-scoped hooks need no turn or item page to be visible.
			for _, p := range h.placements {
				if p.entry.Kind == "hook" {
					entry := p.entry
					entry.Seq = u.session.Activity().NextSequence()
					u.applyRestoredActivity([]sessionpresentation.ActivityEntry{entry})
				}
			}
			h.placements = slices.DeleteFunc(h.placements, func(p *restoredPlacement) bool { return p.entry.Kind == "hook" })
			return u.finishChildHistory()
		}
		return u.requestChildHistory(decision.Next)
	}
	var page binding.ItemPage
	if err := json.Unmarshal(m.Result, &page); err != nil {
		return u.failChildHistory("Invalid item page: " + err.Error())
	}
	ids := make([]sessionpresentation.ItemIdentity, len(page.Data))
	for i, entry := range page.Data {
		ids[i] = sessionpresentation.ItemIdentity{Turn: entry.TurnID, Item: entry.Item.ID}
	}
	decision, err := h.state.Items(ids, page.NextCursor)
	if err != nil {
		return u.failChildHistory(err.Error())
	}
	var items []binding.ThreadItem
	for _, index := range decision.Accepted {
		entry := page.Data[index]
		if at := cmp.Or(entry.CompletedAtMs, entry.StartedAtMs); at != 0 {
			h.itemAt[entry.Item.ID] = time.UnixMilli(at)
		}
		items = append(items, entry.Item)
	}
	// Source: codex-rs/app-server-protocol/src/protocol/v2/thread.rs@de9e78e3e
	// Item anchors are exclusive and turn-scoped. Presentation supplies the
	// chronological positions while the adapter retains protocol payloads.
	turn := h.info.Turns[decision.Turn]
	turn.Items = items
	u.renderChildHistoryPage(h, turn, decision)
	return u.finishChildHistory()
}

func (u *appServerUI) failChildHistory(message string) error {
	h := u.historyLoading()
	h.state.Fail(message)
	if h.state.View(h.info.ID).Initial {
		u.restoreHistoryNotice("Could not restore "+u.session.Caller(h.info.ID)+" history: "+message, false)
		u.restoring.pane[u.session.Caller(h.info.ID)] = h.placements
	} else {
		u.setNotice("Older child history: "+message+" · Ctrl-B o retries", true)
	}
	return u.finishChildHistory()
}

func (u *appServerUI) finishChildHistory() error {
	h := u.historyLoading()
	if u.sessionPresentation().FinishHistory(h.state) {
		u.restoring.state.ReadComplete()
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

// historyViews projects the existing scoped owners in admitted roster order.
// It retains no parallel pagination or transcript state.
func (u *appServerUI) historyViews() []sessionpresentation.HistoryView {
	loading := u.sessionPresentation().View().Loading
	var views []sessionpresentation.HistoryView
	for _, agent := range u.session.Roster() {
		if h := u.childHistory(agent.Thread); h != nil {
			views = append(views, h.state.View(loading))
		}
	}
	return views
}

func (u *appServerUI) updateHistoryHint() {
	if u.agents == nil {
		return
	}
	u.agents.HistoryHint = ""
	if h := u.historyLoading(); h != nil && !h.state.View(h.info.ID).Initial {
		u.agents.HistoryHint = "Loading older · Esc cancels"
	} else if h := u.childHistory(u.sessionPresentation().HistoryView(u.historyViews()).Target); h != nil {
		u.agents.HistoryHint = "Older history · Ctrl-B o load"
		if h.state.View("").Status == sessionpresentation.Failed {
			u.agents.HistoryHint = "History unavailable · Ctrl-B o retry"
		}
	}
}

func (u *appServerUI) loadOlderActivity(intent sessionpresentation.HistoryIntent) error {
	defer u.settlePresentation()
	if u.restoring != nil {
		return nil
	}
	h := u.childHistory(intent.Thread)
	if intent.Thread == "" {
		return nil
	}
	model := u.sessionPresentation()
	var state *sessionpresentation.History
	if h != nil {
		state = h.state
	}
	request, err := model.LoadHistory(intent, state)
	if err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	if request.Kind == sessionpresentation.NoPage {
		return nil
	}
	return u.requestChildHistory(request)
}

func (u *appServerUI) cancelOlderActivity(intent sessionpresentation.HistoryIntent) error {
	defer u.settlePresentation()
	h := u.childHistory(intent.Thread)
	if intent.Thread == "" {
		return nil
	}
	model := u.sessionPresentation()
	var state *sessionpresentation.History
	if h != nil {
		state = h.state
	}
	id, err := model.CancelHistory(intent, state)
	if err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	if id == "" {
		return nil
	}
	u.requests.Forget(id) // Cancel observation, never host execution.
	u.setNotice("Older child history loading cancelled", false)
	return u.finishChildHistory()
}

func (u *appServerUI) holdOlderHistoryEvent(m appserver.Message) (bool, error) {
	h := u.historyLoading()
	if h == nil || m.Method == "" {
		return false, nil
	}
	var params struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(m.Params, &params)
	delta := strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta")
	switch h.state.BufferEvent(params.ThreadID, delta, len(h.pending)) {
	case sessionpresentation.DiscardDelta:
		return true, nil
	case sessionpresentation.Buffer:
		h.pending = append(h.pending, m)
		return true, nil
	case sessionpresentation.CancelRead:
		if err := u.cancelOlderActivity(sessionpresentation.HistoryIntent{Version: u.sessionPresentation().View().Version, Thread: h.info.ID}); err != nil {
			return true, err
		}
	}
	return false, nil
}

func (u *appServerUI) renderChildHistoryPage(h *appServerChildHistory, turn binding.HistoryTurn, page sessionpresentation.ItemPage) {
	var selected, remaining []*restoredPlacement
	positions := historyPositions(h.info.Turns, h.itemAt)
	pagePosition := historyPositions([]binding.HistoryTurn{turn}, h.itemAt)[0]
	for _, p := range h.placements {
		anchored := p.position().InPage(positions, pagePosition, page.Turn, page.Exhausted)
		if anchored {
			selected = append(selected, p)
		} else {
			remaining = append(remaining, p)
		}
	}
	h.placements = remaining
	name := u.session.Caller(h.info.ID)
	r := u.restoring
	if r == nil {
		r = &appServerActivityRestore{state: sessionpresentation.NewRestoration(u.Thread), pane: make(map[string][]*restoredPlacement), itemAt: make(map[string]map[string]time.Time)}
		u.restoring = r
		defer func() { u.restoring = nil }()
	}
	wasPaging := r.paging
	r.paging = true
	defer func() { r.paging = wasPaging }()
	r.pane[name], r.itemAt[h.info.ID] = selected, h.itemAt
	before := u.childSource.LastSeq
	info := h.info
	info.Turns = []binding.HistoryTurn{turn}
	// Historical pages may not reset a newer live lifecycle or cumulative
	// counters. Initial metadata supplies the complete turn count and timing.
	agent := *u.session.Agent(name)
	u.restoreActivityThread(info, page.Older)
	*u.session.Agent(name) = agent
	u.childSource.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: u.session.Roster()})
	first := uint64(0)
	for _, entry := range u.childSource.Entries {
		if entry.Source.Seq > before {
			first = entry.Source.Seq
			break
		}
	}
	if !h.state.View(h.info.ID).Initial {
		retained := slices.ContainsFunc(u.childSource.Entries, func(entry sessionpresentation.ActivityRecord) bool { return entry.Source.Seq == h.firstSeq })
		u.insertActivityHistory(u.childSource, u.agents, before, func(entry sessionpresentation.ActivityEntry) bool {
			return retained && entry.Seq == h.firstSeq || !retained && entry.Native != nil && entry.Native.Thread == h.info.ID
		})
		for _, p := range r.main {
			// A failed initial read may already have placed this retained
			// assignment/message in Main without its Activity link. The same
			// source observation keeps its identity through the pending slots.
			existing := slices.IndexFunc(u.source.Entries, func(entry sessionpresentation.ActivityRecord) bool {
				return p.entry.Assignment != nil && entry.Source.Assignment == p.entry.Assignment || p.entry.Message != nil && entry.Source.Message == p.entry.Message
			})
			if existing >= 0 {
				if p.link != nil && sessionpresentation.MainActivityLinked(p.entry) {
					entry := u.source.Entries[existing].Source
					entry.ActivitySeq = *p.link
					u.source.Model.Replace(existing, entry, sessionpresentation.ActivityContent(entry).Values())
				}
				continue
			}
			before := u.source.LastSeq
			u.applyRestoredMain([]*restoredPlacement{p})
			u.insertActivityHistory(u.source, u.view, before, func(entry sessionpresentation.ActivityEntry) bool { return entry.Observed.After(p.at) })
		}
	}
	if first != 0 {
		h.firstSeq = first
	}
}

func (u *appServerUI) hydrateChildHistoryMetadata(h *appServerChildHistory) {
	agent := u.session.Agent(u.session.Caller(h.info.ID))
	if !h.state.View(h.info.ID).Live {
		var final *bool
		if len(h.info.Turns) > 0 {
			final = new(h.info.Turns[len(h.info.Turns)-1].Status == "completed")
		}
		u.session.Lifecycle(h.info.ID).RestorePage(restoredAgentIntervals(h.info.Turns), uint64(len(h.info.Turns)), final)
	}
	restoreContextUsage(agent, h.info)
	u.restoreUsage(h.info)
	u.observeCost(h.info.ID, agent)
	u.childSource.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: u.session.Roster()})
}

// Insert the admitted source page through the shared model and terminal owners.
// Domain membership stays with the source owner; output demands use normal hydration.
func (u *appServerUI) insertActivityHistory(source *sessionpresentation.ActivitySource, view *activityui.View, before uint64, belongs func(sessionpresentation.ActivityEntry) bool) {
	u.publishActivity()
	anchor := view.AnchorHistory(before)
	for {
		demands := view.OutputDemands()
		if len(demands) == 0 {
			break
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
		anchor = view.AnchorHistory(before)
	}
	source.InsertHistoryPage(before, belongs)
	u.publishActivity()
	for {
		view.HistoryInserted(anchor)
		demands := view.OutputDemands()
		if len(demands) == 0 {
			break
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
	}
}
