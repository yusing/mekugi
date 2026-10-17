package router

import (
	"cmp"
	"iter"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	json "encoding/json/v2"
	"fmt"
	"slices"
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

func (u *appServerUI) restorePaneContent(root binding.ThreadInfo) error {
	u.restoring = &appServerActivityRestore{root: root, threads: make(map[string]binding.ThreadInfo), state: sessionpresentation.NewRestoration(root.ID)}
	u.restoreDiffThread(root, true)
	u.sessionPresentation().Status.SetText("Restoring roster and Activity…")
	return u.restoreList()
}

func (u *appServerUI) restoreDiffThread(info binding.ThreadInfo, root bool) {
	if u.shell == nil || u.shell.review == nil {
		return
	}
	u.shell.review.IncludeThread(info.Cwd, info.ID, root)
	if root {
		u.shell.saved.workspace = info.Cwd
		u.shell.publishReview()
	}
}

func (u *appServerUI) restoreList() error {
	view := u.restoring.state.View()
	params := map[string]any{
		"ancestorThreadId": u.Thread, "modelProviders": []string{},
		"sourceKinds": []string{"subAgentThreadSpawn"}, "limit": 100,
		"archived": view.Archived,
	}
	if view.Cursor != "" {
		params["cursor"] = view.Cursor
	}
	return u.request("thread/list", params)
}

func (u *appServerUI) restoreContentNotice(message string) {
	u.restoreHistoryNotice(message, true)
}

func (u *appServerUI) restoreHistoryNotice(message string, incomplete bool) {
	if incomplete {
		u.agents.StatusLabel = "History incomplete"
	}
	if r := u.restoring; r != nil && !r.state.View().Rendered {
		r.notices = append(r.notices, restoredHistoryNotice{message, incomplete}) // Shown after Main's history.
		return
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.LastSeq + 1, Agent: "Session", Kind: "text", Text: message, Observed: time.Now()}}})
	// Notices advance Main independently of the Activity sequence.
	u.session.seq = max(u.session.seq, u.view.LastSeq)
}

func (u *appServerUI) restoreActivityResponse(method string, m appserver.Message) error {
	r := u.restoring
	if r == nil {
		return nil
	}
	if m.Error != nil {
		u.restoreContentNotice("Could not restore " + method + " history: " + m.Error.Message)
		if method == "thread/list" {
			return u.readRestoredChildren()
		}
		r.state.ReadComplete()
		return u.readNextRestoredChild()
	}
	if method == "thread/list" {
		var result binding.ThreadPage
		if err := json.Unmarshal(m.Result, &result); err != nil {
			u.restoreContentNotice("Invalid roster history: " + err.Error())
			return u.readRestoredChildren()
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
			u.restoreContentNotice(notice)
		}
		if r.state.View().Listed {
			return u.readRestoredChildren()
		}
		return u.restoreList()
	}
	view := r.state.View()
	info, err := binding.DecodeHistoryThread(m.Result, view.Threads[view.Next])
	if err != nil {
		u.restoreContentNotice("Invalid Activity history: " + err.Error())
	} else if info.HistoryMode == "paginated" {
		return u.startChildHistory(info)
	} else {
		u.restoreActivityThread(info, false)
	}
	r.state.ReadComplete()
	return u.readNextRestoredChild()
}

func (u *appServerUI) readRestoredChildren() error {
	r := u.restoring
	r.state.EndDiscovery()
	// Register names before rendering cross-agent assignments and messages.
	for _, id := range r.state.View().Threads {
		info := r.threads[id]
		u.session.registerThread(info)
		u.restoreDiffThread(info, false)
		agent := u.session.agent(u.session.paths[id])
		agent.RestoreTiming(restoredAgentIntervals(info.Turns))
	}
	u.readRestoredRollouts()
	for _, turn := range r.root.Turns {
		for _, item := range turn.Items {
			if item.Type == "collabAgentToolCall" {
				item.SenderThreadID = u.Thread
				entries := u.session.collab(item.Observation(), item.ID, historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, r.root.UpdatedAt)), activitypresentation.HistoricalCollaboration)
				u.applyRestoredActivity(entries)
				for _, entry := range entries {
					if main, ok := mainActivityEntry(entry); ok {
						seq := u.agents.entrySeq(entry)
						r.main = append(r.main, &restoredPlacement{entry: main, turn: turn.ID, anchor: item.ID, at: entry.Observed, link: &seq})
					}
				}
			}
		}
	}
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
	return u.readNextRestoredChild()
}

// readRestoredRollouts reads each thread's retained rollout once every name
// is known. Evidence belongs to the Activity agent live display would show
// it under; the root's own evidence belongs to Main.
func (u *appServerUI) readRestoredRollouts() {
	r := u.restoring
	r.itemAt, r.pane = make(map[string]map[string]time.Time), make(map[string][]*restoredPlacement)
	var activity *subagentActivity
	if u.proxy != nil {
		activity = u.proxy.activity
	}
	infos := []binding.ThreadInfo{r.root}
	for _, id := range r.state.View().Threads {
		infos = append(infos, r.threads[id])
	}
	for _, info := range infos {
		agent := u.session.path(info.ID)
		rollout := readRestoredRollout(info, agent)
		rollout.events = append(rollout.events, u.restoredHookEvents(info)...)
		r.itemAt[info.ID] = rollout.itemAt
		for _, event := range rollout.events {
			if event.repeated {
				activity.markRestored(info.ID, event.entry)
			}
			p := &restoredPlacement{entry: event.entry, turn: event.turn, anchor: event.anchor, at: event.at}
			p.entry.Observed, p.entry.Text = event.at, clipNativeActivityMessage(event.entry.Text)
			owner := paneActivityAgent(event.entry)
			if owner == "/root" {
				p.entry, _ = mainActivityEntry(p.entry)
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

func (u *appServerUI) readNextRestoredChild() error {
	r := u.restoring
	view := r.state.View()
	if view.Next < len(view.Threads) {
		u.sessionPresentation().Status.SetText(fmt.Sprintf("Restoring Activity %d/%d…", view.Next+1, len(view.Threads)))
		return u.request("thread/read", map[string]any{"threadId": view.Threads[view.Next], "includeTurns": r.threads[view.Threads[view.Next]].HistoryMode != "paginated"})
	}
	if !view.Rendered {
		// Evidence for a child whose history could not be read still reaches Main.
		for _, placements := range r.pane {
			for _, p := range placements {
				if main, ok := mainActivityEntry(p.entry); ok {
					r.main = append(r.main, &restoredPlacement{entry: main, at: p.at})
				}
			}
		}
		r.pane = nil
		u.restoreMainHistory(r.root.Turns, r.main, r.itemAt[r.root.ID])
		r.root.Turns, r.main = nil, nil
		r.state.ContentRendered()
		for _, notice := range r.notices {
			u.restoreHistoryNotice(notice.message, notice.incomplete)
		}
		r.notices = nil
	}
	return u.finishRestoredContent()
}

// Scope publication and durable hydration are separate events. Keep Enter
// guarded until the controller has consumed the final scope, not just until
// Codex's history replies have arrived.
func (u *appServerUI) finishRestoredContent() error {
	r := u.restoring
	if r == nil || !r.state.Ready() {
		return nil
	}
	if u.shell != nil && u.shell.review != nil && u.shell.saved.store != nil && u.shell.diffFailure == "" {
		if loader := u.shell.reviewLoader; loader != nil && (loader.active || len(loader.pending) > 0 || loader.resync) {
			u.sessionPresentation().Status.SetText("Restoring saved Diff…")
			return nil
		}
		a := u.shell.review
		scope := a.Scope()
		for workspace, threads := range scope.Workspaces {
			for thread := range threads {
				if !u.shell.saved.scope.Workspaces[workspace][thread] {
					u.sessionPresentation().Status.SetText("Restoring saved Diff…")
					return nil
				}
			}
		}
	}
	u.restoring = nil
	u.updateHistoryHint()
	u.sessionPresentation().Status.SetText("Ready")
	if u.Turn != "" {
		u.sessionPresentation().Status.SetText("Working")
	}
	return u.replayResumePending()
}

func historyTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func (u *appServerUI) applyRestoredActivity(entries []activityPaneEntry) {
	for i := range entries {
		entries[i].Agent = paneActivityAgent(entries[i])
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: entries, Agents: slices.Clone(u.session.agents)})
	u.applyCapturedEdits()
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
func (u *appServerUI) restoreActivityThread(info binding.ThreadInfo, olderPage bool) {
	previousSegments := u.restoredSegments
	defer func() { u.restoredSegments = previousSegments }()
	s := &u.session
	name := s.paths[info.ID]
	agent := s.agent(name)
	u.restoreContextUsage(agent, info)
	u.restoreUsage(info)
	agent.RestoreTiming(restoredAgentIntervals(info.Turns))
	agent.Turns, agent.Responding = uint64(len(info.Turns)), false
	u.observeCost(info.ID, agent)
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(s.agents)})
	var r *appServerActivityRestore
	var itemAt map[string]time.Time
	byTurn := make(map[int][]*restoredPlacement)
	if r = u.restoring; r != nil {
		itemAt = r.itemAt[info.ID]
		positions := historyPositions(info.Turns, itemAt)
		for _, p := range r.pane[name] {
			index := p.position().TurnFor(positions)
			if index >= 0 {
				byTurn[index] = append(byTurn[index], p)
			} else if main, ok := mainActivityEntry(p.entry); ok && p.turn == "" {
				r.main = append(r.main, &restoredPlacement{entry: main, at: p.at})
			} else if p.entry.Kind == "hook" {
				entry := p.entry
				entry.Seq = s.next()
				u.applyRestoredActivity([]activityPaneEntry{entry})
			}
		}
		delete(r.pane, name)
	}
	positions := historyPositions(info.Turns, itemAt)
	for index, turn := range info.Turns {
		u.restoredSegments = u.prepareCommandSegments(info.ID, info.Cwd, turn)
		observed := historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, info.UpdatedAt))
		var entries []activityPaneEntry
		var times []time.Time // When each entry happened, to place Main's copy.
		placements := byTurn[index]
		place := func(slot []int) {
			for _, index := range slot {
				p := placements[index]
				entry := p.entry
				entry.Seq = s.next()
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
			u.restoreActivityItem(info, turn, item, name, at, &entries, text)
			for range entries[first:] {
				times = append(times, at)
			}
			place(slots[i+1])
		}
		agent = s.agent(name)
		agent.Final = turn.Status == "completed"
		if !olderPage && (turn.Status == "failed" || turn.Status == "interrupted" || turn.Status == "inProgress") {
			text := "Recorded turn: " + turn.Status + " (history only)"
			if turn.Error != nil {
				text += ": " + turn.Error.Message
			}
			entries = append(entries, activityPaneEntry{Seq: s.next(), Agent: name, Kind: "error", Text: text, Observed: observed})
			times = append(times, observed)
		}
		u.applyRestoredActivity(entries)
		if r == nil {
			continue
		}
		for i, entry := range entries {
			if main, ok := mainActivityEntry(entry); ok {
				seq := u.agents.entrySeq(entry)
				r.main = append(r.main, &restoredPlacement{entry: main, at: times[i], link: &seq})
			}
		}
	}
}

func (u *appServerUI) restoreActivityItem(info binding.ThreadInfo, turn binding.HistoryTurn, item binding.ThreadItem, name string, observed time.Time, entries *[]activityPaneEntry, text map[string]observation.TextUpdate) {
	facts := item.Observation()
	s := &u.session
	if update, ok := text[item.ID]; ok {
		update.Sequence, update.Observed = s.next(), observed
		entry := transcriptEntry(update, name)
		entry.CallID = item.ID
		*entries = append(*entries, entry)
		return
	}
	entry := activityPaneEntry{Seq: s.next(), Agent: name, Observed: observed, CallID: item.ID,
		native: &liveActivityNativeItem{thread: info.ID, turn: turn.ID, item: item.ID, phase: "item/completed", status: item.Status, searchResults: appServerSearchResults(facts)}}
	progressPhase := appServerHistoryProgressPhase(facts, turn.Status)
	if text, wait, handled := u.itemProgress(facts, info.ID, turn.ID, item.ID, progressPhase, false); handled {
		if text != "" {
			entry.Kind, entry.Text = "progress", text
			entry.native.wait = wait
			entry.native.phase = progressPhase
			*entries = append(*entries, entry)
		}
		return
	}
	switch item.Type {
	case "userMessage":
		if blocks := appServerAttachmentValues(info.Cwd, item.Content); len(blocks) > 0 {
			entry.Kind, entry.native.item = "attachments", item.ID+"/attachments"
			entry.native.attachments = blocks
			*entries = append(*entries, entry)
		}
	case "imageView":
		entry.Kind, entry.Text = "tool", "View "+markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(info.Cwd, item.Path))
		*entries = append(*entries, entry)
	case "commandExecution", "fileChange", "webSearch":
		entry.Kind, entry.Text = "tool", appServerToolText(facts, info.Cwd)
		entry.native.command, entry.native.duration = item.Command, appServerDuration(facts)
		entry.native.commandCwd = appServerCommandDirectory(facts, info.Cwd)
		entry.native.workdir = appServerCommandWorkdir(facts, info.Cwd)
		if item.Type == "fileChange" {
			entry.Text = activitypresentation.HostEditText(facts, info.Cwd)
			entry.native.editPages = activitypresentation.HostEditPages(facts, info.Cwd, entry.native.phase)
		}
		s.completeCommand(entry.native, facts)
		appServerCommandOutput(&entry, facts, time.Time{})
		u.restoreCommandSegments(&entry, item, info.Cwd)
		*entries = append(*entries, entry)
		if len(entry.native.segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := activityPaneEntry{Seq: s.next(), Agent: name, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID, Observed: observed}
			exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
			*entries = append(*entries, exit)
		}
	case "collabAgentToolCall":
		*entries = append(*entries, u.session.collab(facts, item.ID, observed, activitypresentation.HistoricalCollaboration)...)
	}
}
