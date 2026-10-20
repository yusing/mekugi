package launch

import (
	"cmp"
	"context"
	"iter"

	"github.com/yusing/mekugi/modules/conversation/composer"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	json "encoding/json/v2"
	"fmt"
	"strconv"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

type restoredHistoryNotice struct {
	message    string
	incomplete bool // Permanent discovery/evidence gap, not a retryable item read.
}

type appServerActivityRestore struct {
	root    binding.ThreadInfo
	threads map[string]binding.ThreadInfo
	state   *sessionpresentation.Restoration
	// Rollout evidence the host's history omits: item completion times by
	// thread ID, Activity placements by agent path, and Main placements,
	// which wait for every child so Main can follow them in time order.
	itemAt  map[string]map[string]time.Time
	pane    map[string][]*restoredPlacement
	main    []*restoredPlacement
	notices []restoredHistoryNotice
	paging  bool // Bind cumulative answers only after a page has its chronological position.
}

func restorePaneContent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], restoring **appServerActivityRestore, reviewOwner *sessionreview.Review, reviewSource *changecapture.Service, preparePane func(*scope.Store), root binding.ThreadInfo) error {
	(*restoring) = &appServerActivityRestore{root: root, threads: make(map[string]binding.ThreadInfo), state: sessionpresentation.NewRestoration(root.ID)}
	restoreDiffThread(attached, terminal, reviewOwner, reviewSource, preparePane, root, true)
	attached.presentation.Status.SetText("Restoring roster and Activity…")
	return restoreList(control, (*restoring))
}

func restoreDiffThread(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), info binding.ThreadInfo, root bool) {
	if review == nil {
		return
	}
	review.IncludeThread(info.Cwd, info.ID, root)
	if root {
		review.Projection.Workspace = info.Cwd
		publishReview(attached, terminal, review, source, prepare)
	}
}

func restoreList(control *sessioncontrol.Client[appServerRequest], restoring *appServerActivityRestore) error {
	view := restoring.state.View()
	params := map[string]any{
		"ancestorThreadId": control.Thread, "modelProviders": []string{},
		"sourceKinds": []string{"subAgentThreadSpawn"}, "limit": 100,
		"archived": view.Archived,
	}
	if view.Cursor != "" {
		params["cursor"] = view.Cursor
	}
	return control.Request("thread/list", params)
}

func restoreHistoryNotice(attached *attachedPresentation, restoring *appServerActivityRestore, message string, incomplete bool) {
	if incomplete {
		attached.childSource.Coverage = "History incomplete"
	}
	if r := restoring; r != nil && !r.state.View().Rendered {
		r.notices = append(r.notices, restoredHistoryNotice{message, incomplete}) // Shown after Main's history.
		return
	}
	attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: message, Observed: time.Now()}}})
	// Notices advance Main independently of the Activity sequence.
	attached.session.Activity().AdvanceSequence(attached.source.LastSeq)
}

func restoreActivityResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, audience *sessioncontrol.Audience, reviewSource *changecapture.Service, preparePane func(*scope.Store), activityService *activitymodel.Service, usage *accounting.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, views *sessionpresentation.Service, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, method string, m appserver.Message) error {
	r := (*restoring)
	if r == nil {
		return nil
	}
	if m.Error != nil {
		restoreHistoryNotice(attached, (*restoring), "Could not restore "+method+" history: "+m.Error.Message, true)
		if method == "thread/list" {
			return readRestoredChildren(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, audience, reviewSource, preparePane, activityService, segments, restoredSegments, dispatch, resumePending)
		}
		r.state.ReadComplete()
		return readNextRestoredChild(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
	}
	if method == "thread/list" {
		var result binding.ThreadPage
		if err := json.Unmarshal(m.Result, &result); err != nil {
			restoreHistoryNotice(attached, (*restoring), "Invalid roster history: "+err.Error(), true)
			return readRestoredChildren(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, audience, reviewSource, preparePane, activityService, segments, restoredSegments, dispatch, resumePending)
		}
		ids := make([]string, len(result.Data))
		for i, info := range result.Data {
			ids[i] = info.ID
		}
		accepted, notice := r.state.Discover(ids, result.NextCursor)
		for _, index := range accepted {
			info := result.Data[index]
			r.threads[info.ID] = info
		}
		if notice != "" {
			restoreHistoryNotice(attached, (*restoring), notice, true)
		}
		if r.state.View().Listed {
			return readRestoredChildren(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, audience, reviewSource, preparePane, activityService, segments, restoredSegments, dispatch, resumePending)
		}
		return restoreList(control, (*restoring))
	}
	view := r.state.View()
	info, err := binding.DecodeHistoryThread(m.Result, view.Threads[view.Next])
	if err != nil {
		restoreHistoryNotice(attached, (*restoring), "Invalid Activity history: "+err.Error(), true)
	} else if info.HistoryMode == "paginated" {
		return startChildHistory(attached, control, childKey, restoring, views, info)
	} else {
		restoreActivityThread(attached, terminal, clock, restoring, parent, (*reviewOwner), receipts, retained, (*retainedSession), activityService, usage, segments, restoredSegments, info, false)
	}
	r.state.ReadComplete()
	return readNextRestoredChild(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
}

func readRestoredChildren(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, audience *sessioncontrol.Audience, reviewSource *changecapture.Service, preparePane func(*scope.Store), activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	r := (*restoring)
	r.state.EndDiscovery()
	// Register names before rendering cross-agent assignments and messages.
	for _, id := range r.state.View().Threads {
		info := r.threads[id]
		registerThread(&attached.session, info)
		restoreDiffThread(attached, terminal, (*reviewOwner), reviewSource, preparePane, info, false)
		attached.session.Lifecycle(id).RestoreTiming(restoredAgentIntervals(info.Turns))
	}
	readRestoredRollouts(attached, restoring, (*retainedSession), audience, activityService)
	for _, turn := range r.root.Turns {
		for _, item := range turn.Items {
			if item.Type == "collabAgentToolCall" {
				item.SenderThreadID = control.Thread
				entries := attached.session.Collaboration(item.Observation(), item.ID, historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, r.root.UpdatedAt)), activitypresentation.HistoricalCollaboration)
				applyRestoredActivity(attached, (*reviewOwner), receipts, entries)
				for _, entry := range entries {
					if main, ok := sessionpresentation.MainActivityEntry(entry); ok {
						seq := attached.childSource.EntrySeq(entry)
						r.main = append(r.main, &restoredPlacement{entry: main, turn: turn.ID, anchor: item.ID, at: entry.Observed, link: &seq})
					}
				}
			}
		}
	}
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
	return readNextRestoredChild(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending)
}

// readRestoredRollouts reads each thread's retained rollout once every name
// is known. Evidence belongs to the Activity agent live display would show
// it under; the root's own evidence belongs to Main.
func readRestoredRollouts(attached *attachedPresentation, restoring **appServerActivityRestore, retainedSession history.Session, audience *sessioncontrol.Audience, activityService *activitymodel.Service) {
	r := (*restoring)
	r.itemAt, r.pane = make(map[string]map[string]time.Time), make(map[string][]*restoredPlacement)
	activity := audience
	infos := []binding.ThreadInfo{r.root}
	for _, id := range r.state.View().Threads {
		infos = append(infos, r.threads[id])
	}
	for _, info := range infos {
		agent := attached.session.Caller(info.ID)
		rollout := readRestoredRollout(info, agent)
		rollout.events = append(rollout.events, restoredHookEvents(attached, activityService, retainedSession, (*restoring), info)...)
		r.itemAt[info.ID] = rollout.itemAt
		for _, event := range rollout.events {
			if event.repeated {
				activity.Restore(activitypresentation.Supplement{Thread: info.ID, Kind: event.entry.Kind, Assignment: sessionpresentation.SupplementAssignment(event.entry.Assignment), Message: sessionpresentation.SupplementMessage(event.entry.Message)})
			}
			p := &restoredPlacement{entry: event.entry, turn: event.turn, anchor: event.anchor, at: event.at}
			p.entry.Observed, p.entry.Text = event.at, activitypresentation.ClipSupplementMessage(event.entry.Text)
			owner := sessionpresentation.ActivityOwner(event.entry)
			if owner == "/root" {
				p.entry, _ = sessionpresentation.MainActivityEntry(p.entry)
				r.main = append(r.main, p)
				continue
			}
			p.entry.Agent = owner
			if owner != agent {
				p.turn, p.anchor = "", "" // Another thread's history orders it by time.
			}
			r.pane[owner] = append(r.pane[owner], p)
		}
	}
}

func readNextRestoredChild(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	r := (*restoring)
	view := r.state.View()
	if view.Next < len(view.Threads) {
		attached.presentation.Status.SetText(fmt.Sprintf("Restoring Activity %d/%d…", view.Next+1, len(view.Threads)))
		return control.Request("thread/read", map[string]any{"threadId": view.Threads[view.Next], "includeTurns": r.threads[view.Threads[view.Next]].HistoryMode != "paginated"})
	}
	if !view.Rendered {
		// Evidence for a child whose history could not be read still reaches Main.
		for _, placements := range r.pane {
			for _, p := range placements {
				if main, ok := sessionpresentation.MainActivityEntry(p.entry); ok {
					r.main = append(r.main, &restoredPlacement{entry: main, at: p.at})
				}
			}
		}
		r.pane = nil
		restoreMainHistory(attached, terminal, control, picker, editor, questions, approvals, clock, panel, resumePicker, parent, (*reviewOwner), receipts, notifications, retained, (*retainedSession), activityService, segments, restoredSegments, r.root.Turns, r.main, r.itemAt[r.root.ID])
		r.root.Turns, r.main = nil, nil
		r.state.ContentRendered()
		for _, notice := range r.notices {
			restoreHistoryNotice(attached, (*restoring), notice.message, notice.incomplete)
		}
		r.notices = nil
	}
	return finishRestoredContent(attached, control, restoring, reviewOwner, dispatch, resumePending)
}

// Scope publication and durable hydration are separate events. Keep Enter
// guarded until the controller has consumed the final scope, not just until
// Codex's history replies have arrived.
func finishRestoredContent(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], restoring **appServerActivityRestore, reviewOwner **sessionreview.Review, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	r := (*restoring)
	if r == nil || !r.state.Ready() {
		return nil
	}
	if (*reviewOwner) != nil && (*reviewOwner).Restoring() {
		attached.presentation.Status.SetText("Restoring saved Diff…")
		return nil
	}

	(*restoring) = nil
	attached.presentation.Status.SetText("Ready")
	if control.Turn != "" {
		attached.presentation.Status.SetText("Working")
	}
	return replayResumePending(dispatch, resumePending)
}

func historyTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func applyRestoredActivity(attached *attachedPresentation, review *sessionreview.Review, pending *[]*changepresentation.Receipt, entries []sessionpresentation.ActivityEntry) {
	for i := range entries {
		entries[i].Agent = sessionpresentation.ActivityOwner(entries[i])
	}
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Entries: entries, Agents: attached.session.Roster()})
	applyCapturedEdits(attached, review, pending)
}

func restoredAgentIntervals(turns []binding.HistoryTurn) iter.Seq2[time.Time, time.Time] {
	return func(yield func(time.Time, time.Time) bool) {
		for _, turn := range turns {
			if !yield(historyTime(turn.StartedAt), historyTime(turn.CompletedAt)) {
				return
			}
		}
	}
}

// Observational history never opens previews, resumes children, or fabricates
// a live response. Only a subsequent live notification can mark an agent busy.
func restoreActivityThread(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, restoring **appServerActivityRestore, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, retained changes.Scope, retainedSession history.Session, activityService *activitymodel.Service, usage *accounting.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, info binding.ThreadInfo, olderPage bool) {
	previousSegments := (*restoredSegments)
	defer func() { (*restoredSegments) = previousSegments }()
	s := &attached.session
	name := s.KnownCaller(info.ID)
	agent := s.Agent(name)
	restoreContextUsage(agent, info)
	restoreUsage(usage, info)
	page := (*restoring) != nil && (*restoring).paging
	if !page {
		s.Lifecycle(info.ID).Restore(restoredAgentIntervals(info.Turns), uint64(len(info.Turns)), false)
	}
	observeCost(usage, info.ID, agent)
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: s.Roster()})
	var r *appServerActivityRestore
	var itemAt map[string]time.Time
	byTurn := make(map[int][]*restoredPlacement)
	if r = (*restoring); r != nil {
		itemAt = r.itemAt[info.ID]
		positions := historyPositions(info.Turns, itemAt)
		for _, p := range r.pane[name] {
			index := p.position().TurnFor(positions)
			if index >= 0 {
				byTurn[index] = append(byTurn[index], p)
			} else if main, ok := sessionpresentation.MainActivityEntry(p.entry); ok && p.turn == "" {
				r.main = append(r.main, &restoredPlacement{entry: main, at: p.at})
			} else if p.entry.Kind == "hook" {
				entry := p.entry
				entry.Seq = s.Activity().NextSequence()
				applyRestoredActivity(attached, reviewOwner, receipts, []sessionpresentation.ActivityEntry{entry})
			}
		}
		delete(r.pane, name)
	}
	positions := historyPositions(info.Turns, itemAt)
	for index, turn := range info.Turns {
		(*restoredSegments) = prepareCommandSegments(parent, retained, segments, info.ID, info.Cwd, turn)
		observed := historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, info.UpdatedAt))
		var entries []sessionpresentation.ActivityEntry
		var times []time.Time // When each entry happened, to place Main's copy.
		placements := byTurn[index]
		place := func(slot []int) {
			for _, index := range slot {
				p := placements[index]
				entry := p.entry
				entry.Seq = s.Activity().NextSequence()
				entries, times = append(entries, entry), append(times, p.at)
			}
		}
		slots := positions[index].Slots(restoredPositions(placements))
		place(slots[0])
		text := historicalTranscript(turn.Items, info.ID, turn.ID, false, turn.Status == "completed" && !olderPage, observed)
		for i, item := range turn.Items {
			first := len(entries)
			at, ok := itemAt[item.ID]
			if !ok {
				at = observed
			}
			restoreActivityItem(attached, terminal, clock, parent, retained, retainedSession, activityService, segments, restoredSegments, info, turn, item, name, at, &entries, text)
			for range entries[first:] {
				times = append(times, at)
			}
			place(slots[i+1])
		}
		if !page {
			s.Lifecycle(info.ID).RestoreFinal(turn.Status == "completed")
		}
		if !olderPage && (turn.Status == "failed" || turn.Status == "interrupted" || turn.Status == "inProgress") {
			text := "Recorded turn: " + turn.Status + " (history only)"
			if turn.Error != nil {
				text += ": " + turn.Error.Message
			}
			entries = append(entries, sessionpresentation.ActivityEntry{Seq: s.Activity().NextSequence(), Agent: name, Kind: "error", Text: text, Observed: observed})
			times = append(times, observed)
		}
		applyRestoredActivity(attached, reviewOwner, receipts, entries)
		if r == nil {
			continue
		}
		for i, entry := range entries {
			if main, ok := sessionpresentation.MainActivityEntry(entry); ok {
				seq := attached.childSource.EntrySeq(entry)
				r.main = append(r.main, &restoredPlacement{entry: main, at: times[i], link: &seq})
			}
		}
	}
}

func restoreActivityItem(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, parent context.Context, retained changes.Scope, retainedSession history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, info binding.ThreadInfo, turn binding.HistoryTurn, item binding.ThreadItem, name string, observed time.Time, entries *[]sessionpresentation.ActivityEntry, text map[string]observation.TextUpdate) {
	facts := item.Observation()
	s := &attached.session
	if update, ok := text[item.ID]; ok {
		update.Sequence, update.Observed = s.Activity().NextSequence(), observed
		entry := sessionpresentation.TranscriptEntry(update, name)
		entry.CallID = item.ID
		*entries = append(*entries, entry)
		return
	}
	entry := sessionpresentation.ActivityEntry{Seq: s.Activity().NextSequence(), Agent: name, Observed: observed, CallID: item.ID,
		Native: &sessionpresentation.NativeItem{Thread: info.ID, Turn: turn.ID, Item: item.ID, Phase: "item/completed", Status: item.Status, SearchResults: sessionpresentation.SearchResults(facts)}}
	progressPhase := appServerHistoryProgressPhase(facts, turn.Status)
	if text, wait, handled := itemProgress(attached, terminal, retainedSession, clock, facts, info.ID, turn.ID, item.ID, progressPhase, false); handled {
		if text != "" {
			entry.Kind, entry.Text = "progress", text
			entry.Native.Wait = wait
			entry.Native.Phase = progressPhase
			*entries = append(*entries, entry)
		}
		return
	}
	switch item.Type {
	case "userMessage":
		if blocks := sessionpresentation.AttachmentValues(info.Cwd, item.Content); len(blocks) > 0 {
			entry.Kind, entry.Native.Item = "attachments", item.ID+"/attachments"
			entry.Native.Attachments = blocks
			*entries = append(*entries, entry)
		}
	case "imageView":
		entry.Kind, entry.Text = "tool", "View "+markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(info.Cwd, item.Path))
		*entries = append(*entries, entry)
	case "commandExecution", "fileChange", "webSearch":
		entry.Kind, entry.Text = "tool", sessionpresentation.ToolText(facts, info.Cwd)
		entry.Native.Command, entry.Native.Duration = item.Command, sessionpresentation.ItemDuration(facts)
		entry.Native.CommandCwd = sessionpresentation.CommandDirectory(facts, info.Cwd)
		entry.Native.Workdir = sessionpresentation.CommandWorkdir(facts, info.Cwd)
		if item.Type == "fileChange" {
			entry.Text = activitypresentation.HostEditText(facts, info.Cwd)
			entry.Native.EditPages = activitypresentation.HostEditPages(facts, info.Cwd, entry.Native.Phase)
		}
		attached.session.CompleteCommand(entry.Native, facts, activityService.Outputs(attached.session.Scope()))
		sessionpresentation.CommandOutput(&entry, facts, time.Time{})
		restoreCommandSegments(parent, retained, segments, attached, activityService, (*restoredSegments), &entry, item, info.Cwd)
		*entries = append(*entries, entry)
		if len(entry.Native.Segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := sessionpresentation.ActivityEntry{Seq: s.Activity().NextSequence(), Agent: name, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID, Observed: observed}
			exit.OutputTail, exit.OutputOmit = sessionpresentation.OutputTail(item.AggregatedOutput)
			*entries = append(*entries, exit)
		}
	case "collabAgentToolCall":
		*entries = append(*entries, attached.session.Collaboration(facts, item.ID, observed, activitypresentation.HistoricalCollaboration)...)
	}
}
