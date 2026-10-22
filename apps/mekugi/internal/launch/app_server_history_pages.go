package launch

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"

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
	presentation *sessionpresentation.Model
	view         string
	state        *sessionpresentation.History
	info         binding.ThreadInfo
	itemAt       map[string]time.Time
	placements   []*restoredPlacement
	firstSeq     uint64
	pending      []appserver.Message
	skills       *appServerSkillHistory
}

func childHistory(session *sessionpresentation.Attachment, key *scope.Key[*appServerChildHistory], thread string) *appServerChildHistory {
	if store := session.FindThread(thread); store != nil {
		return *key.Value(store)
	}
	return nil
}

func historyLoading(attached *attachedPresentation, key *scope.Key[*appServerChildHistory]) *appServerChildHistory {
	if attached.loadingHistory != nil {
		return attached.loadingHistory
	}
	return childHistory(&attached.session, key, attached.presentation.View().Loading)
}

func startChildHistory(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, views *sessionpresentation.Service, info binding.ThreadInfo) error {
	r := (*restoring)
	store := attached.session.Thread(info.ID)
	h := &appServerChildHistory{state: views.History(store), info: info, presentation: attached.presentation,
		itemAt: r.itemAt[info.ID], placements: r.pane[attached.session.Caller(info.ID)]}
	if h.itemAt == nil {
		h.itemAt = make(map[string]time.Time)
	}
	delete(r.pane, attached.session.Caller(info.ID))
	h.info.Turns = nil
	*childKey.Value(store) = h
	attached.loadingHistory = h
	request := attached.presentation.StartHistory(h.state, info.ID)
	attached.childSource.MarkSkillHistoryUnknown(attached.session.Caller(info.ID))
	return requestChildHistory(attached, childKey, control.Requests, (*restoring), request)
}

func requestChildHistory(attached *attachedPresentation, key *scope.Key[*appServerChildHistory], requests *sessioncontrol.Requests[appServerRequest], restoring *appServerActivityRestore, request sessionpresentation.PageRequest) error {
	h := historyLoading(attached, key)
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
	id, err := requests.Send(method, params, "main", appServerRequest{label: "activity/" + method, view: h.view, history: true})
	if err != nil {
		return err
	}
	h.state.BindRequest(id)
	state := h.state.View(request.Thread)
	if state.Initial {
		progress := restoring.state.View()
		if request.Kind == sessionpresentation.TurnMetadata {
			attached.presentation.Status.SetText(fmt.Sprintf("Restoring Activity %d/%d · turn page %d…", progress.Next+1, len(progress.Threads), state.TurnPages+1))
		} else {
			attached.presentation.Status.SetText(fmt.Sprintf("Restoring Activity %d/%d · recent items…", progress.Next+1, len(progress.Threads)))
		}
	}
	return nil
}

func childHistoryResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, usage *accounting.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, method string, m appserver.Message) error {
	h := historyLoading(attached, childKey)
	if h == nil || !h.state.AcceptResponse(string(m.ID)) {
		return nil
	}
	if m.Error != nil {
		return failChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, m.Error.Message)
	}
	if method == "activity/thread/turns/list" {
		var page binding.TurnPage
		if err := json.Unmarshal(m.Result, &page); err != nil {
			return failChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, "Invalid turn page: "+err.Error())
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
			return failChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, err.Error())
		}
		if !decision.Complete {
			return requestChildHistory(attached, childKey, control.Requests, (*restoring), decision.Next)
		}
		if decision.Limited {
			restoreHistoryNotice(attached, (*restoring), "Activity turn metadata is limited to eight pages; older turns and their evidence were not loaded.", true)
		}
		slices.Reverse(h.info.Turns)
		hydrateChildHistoryMetadata(attached, usage, h)
		if decision.Next.Kind == sessionpresentation.NoPage {
			// Thread-scoped hooks need no turn or item page to be visible.
			for _, p := range h.placements {
				if p.entry.Kind == "hook" {
					entry := p.entry
					entry.Seq = attached.session.Activity().NextSequence()
					applyRestoredActivity(attached, (*reviewOwner), receipts, []sessionpresentation.ActivityEntry{entry})
				}
			}
			h.placements = slices.DeleteFunc(h.placements, func(p *restoredPlacement) bool { return p.entry.Kind == "hook" })
			return finishChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
		}
		return requestChildHistory(attached, childKey, control.Requests, (*restoring), decision.Next)
	}
	var page binding.ItemPage
	if err := json.Unmarshal(m.Result, &page); err != nil {
		return failChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, "Invalid item page: "+err.Error())
	}
	ids := make([]sessionpresentation.ItemIdentity, len(page.Data))
	for i, entry := range page.Data {
		ids[i] = sessionpresentation.ItemIdentity{Turn: entry.TurnID, Item: entry.Item.ID}
	}
	decision, err := h.state.Items(ids, page.NextCursor)
	if err != nil {
		return failChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, err.Error())
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
	renderChildHistoryPage(attached, terminal, control, clock, restoring, parent, (*reviewOwner), receipts, retained, (*retainedSession), activityService, usage, segments, restoredSegments, h, turn, decision)
	return finishChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
}

func failChildHistory(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, message string) error {
	h := historyLoading(attached, childKey)
	h.state.Fail(message)
	if h.state.View(h.info.ID).Initial {
		restoreHistoryNotice(attached, (*restoring), "Could not restore "+attached.session.Caller(h.info.ID)+" history: "+message, false)
		(*restoring).pane[attached.session.Caller(h.info.ID)] = h.placements
	} else {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Older child history: "+message+" · Ctrl-B o retries", true)
	}
	return finishChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
}

func finishChildHistory(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	h := historyLoading(attached, childKey)
	model := h.presentation
	if model == nil {
		model = attached.presentation
	}
	attached.loadingHistory = nil
	initial := model.FinishHistory(h.state)
	h.presentation, h.view = nil, ""
	if initial {
		(*restoring).state.ReadComplete()
		return readNextRestoredChild(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
	}
	pending := h.pending
	h.pending = nil
	for _, m := range pending {
		if err := dispatch(m); err != nil {
			return err
		}
	}
	return nil
}

// historyViews projects the existing scoped owners in admitted roster order.
// It retains no parallel pagination or transcript state.
func historyViews(attached *attachedPresentation, key *scope.Key[*appServerChildHistory]) []sessionpresentation.HistoryView {
	loading := attached.presentation.View().Loading
	var views []sessionpresentation.HistoryView
	for _, agent := range attached.session.Roster() {
		if h := childHistory(&attached.session, key, agent.Thread); h != nil {
			views = append(views, h.state.View(loading))
		}
	}
	return views
}

func loadOlderActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, intent sessionpresentation.HistoryIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	if (*restoring) != nil {
		return nil
	}
	if attached.loadingHistory != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the current history read to finish", false)
		return nil
	}
	h := childHistory(&attached.session, childKey, intent.Thread)
	if intent.Thread == "" {
		return nil
	}
	model := attached.presentation
	var state *sessionpresentation.History
	if h != nil {
		state = h.state
	}
	request, err := model.LoadHistory(intent, state)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	if request.Kind == sessionpresentation.NoPage {
		return nil
	}
	h.presentation, h.view = model, attached.view
	attached.loadingHistory = h
	return requestChildHistory(attached, childKey, control.Requests, (*restoring), request)
}

func cancelOlderActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, intent sessionpresentation.HistoryIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	h := childHistory(&attached.session, childKey, intent.Thread)
	if intent.Thread == "" {
		return nil
	}
	viewModel := attached.presentation
	if h != nil && h.view != attached.view {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "This history read belongs to another view", false)
		return nil
	}
	var state *sessionpresentation.History
	if h != nil {
		state = h.state
	}
	id, err := viewModel.CancelHistory(intent, state)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	if id == "" {
		return nil
	}
	control.Requests.Forget(id) // Cancel observation, never host execution.
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Older child history loading cancelled", false)
	return finishChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
}

func holdOlderHistoryEvent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, m appserver.Message) (bool, error) {
	h := historyLoading(attached, childKey)
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
		if h.view != attached.view && h.presentation != nil {
			id, err := h.presentation.CancelHistory(sessionpresentation.HistoryIntent{Version: h.presentation.View().Version, Thread: h.info.ID}, h.state)
			if err != nil {
				return true, err
			}
			control.Requests.Forget(id)
			return false, finishChildHistory(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
		}
		if err := cancelOlderActivity(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, sessionpresentation.HistoryIntent{Version: attached.presentation.View().Version, Thread: h.info.ID}); err != nil {
			return true, err
		}
	}
	return false, nil
}

func renderChildHistoryPage(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], clock func() time.Time, restoring **appServerActivityRestore, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, retained changes.Scope, retainedSession history.Session, activityService *activitymodel.Service, usage *accounting.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, h *appServerChildHistory, turn binding.HistoryTurn, page sessionpresentation.ItemPage) {
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
	name := attached.session.Caller(h.info.ID)
	r := (*restoring)
	if r == nil {
		r = &appServerActivityRestore{state: sessionpresentation.NewRestoration(control.Thread), pane: make(map[string][]*restoredPlacement), itemAt: make(map[string]map[string]time.Time)}
		(*restoring) = r
		defer func() { (*restoring) = nil }()
	}
	wasPaging := r.paging
	r.paging = true
	defer func() { r.paging = wasPaging }()
	r.pane[name], r.itemAt[h.info.ID] = selected, h.itemAt
	before := attached.childSource.LastSeq
	info := h.info
	info.Turns = []binding.HistoryTurn{turn}
	// Historical pages may not reset a newer live lifecycle or cumulative
	// counters. Initial metadata supplies the complete turn count and timing.
	agent := *attached.session.Agent(name)
	restoreActivityThread(attached, terminal, clock, restoring, parent, reviewOwner, receipts, retained, retainedSession, activityService, usage, segments, restoredSegments, info, page.Older)
	*attached.session.Agent(name) = agent
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
	first := uint64(0)
	for _, entry := range attached.childSource.Entries {
		if entry.Source.Seq > before {
			first = entry.Source.Seq
			break
		}
	}
	if !h.state.View(h.info.ID).Initial {
		retained := slices.ContainsFunc(attached.childSource.Entries, func(entry sessionpresentation.ActivityRecord) bool { return entry.Source.Seq == h.firstSeq })
		insertActivityHistory(attached, terminal, attached.childSource, terminal.Agents, before, func(entry sessionpresentation.ActivityEntry) bool {
			return retained && entry.Seq == h.firstSeq || !retained && entry.Native != nil && entry.Native.Thread == h.info.ID
		})
		for _, p := range r.main {
			// A failed initial read may already have placed this retained
			// assignment/message in Main without its Activity link. The same
			// source observation keeps its identity through the pending slots.
			existing := slices.IndexFunc(attached.source.Entries, func(entry sessionpresentation.ActivityRecord) bool {
				return p.entry.Assignment != nil && entry.Source.Assignment == p.entry.Assignment || p.entry.Message != nil && entry.Source.Message == p.entry.Message
			})
			if existing >= 0 {
				if p.link != nil && sessionpresentation.MainActivityLinked(p.entry) {
					entry := attached.source.Entries[existing].Source
					entry.ActivitySeq = *p.link
					attached.source.Model.Replace(existing, entry, sessionpresentation.ActivityContent(entry).Values())
				}
				continue
			}
			before := attached.source.LastSeq
			applyRestoredMain(attached.source, []*restoredPlacement{p})
			insertActivityHistory(attached, terminal, attached.source, terminal.Transcript, before, func(entry sessionpresentation.ActivityEntry) bool { return entry.Observed.After(p.at) })
		}
	}
	if first != 0 {
		h.firstSeq = first
	}
}

func hydrateChildHistoryMetadata(attached *attachedPresentation, usage *accounting.Service, h *appServerChildHistory) {
	agent := attached.session.Agent(attached.session.Caller(h.info.ID))
	if !h.state.View(h.info.ID).Live {
		var final *bool
		if len(h.info.Turns) > 0 {
			final = new(h.info.Turns[len(h.info.Turns)-1].Status == "completed")
		}
		attached.session.Lifecycle(h.info.ID).RestorePage(restoredAgentIntervals(h.info.Turns), uint64(len(h.info.Turns)), final)
	}
	restoreContextUsage(agent, h.info)
	restoreUsage(usage, h.info)
	observeCost(usage, h.info.ID, agent)
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
}

// Insert the admitted source page through the shared model and terminal owners.
// Domain membership stays with the source owner; output demands use normal hydration.
func insertActivityHistory(attached *attachedPresentation, terminal *terminalsession.Attachment, source *sessionpresentation.ActivitySource, view *activityui.View, before uint64, belongs func(sessionpresentation.ActivityEntry) bool) {
	publishActivity(attached, terminal)
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
	publishActivity(attached, terminal)
	for {
		view.HistoryInserted(anchor)
		demands := view.OutputDemands()
		if len(demands) == 0 {
			break
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
	}
}
