package router

import (
	"cmp"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

const appServerRestoreThreadLimit = 128

type restoredHistoryNotice struct {
	message    string
	incomplete bool // Permanent discovery/evidence gap, not a retryable item read.
}

type appServerActivityRestore struct {
	root     appServerThreadInfo
	threads  map[string]appServerThreadInfo
	order    []string
	archived bool
	cursors  map[string]bool
	next     int
	pages    int
	listed   bool
	// Rollout evidence the host's history omits: item completion times by
	// thread ID, Activity placements by agent path, and Main placements,
	// which wait for every child so Main can follow them in time order.
	itemAt       map[string]map[string]time.Time
	journalTurns map[string]journalReplayTurn
	pane         map[string][]*restoredPlacement
	main         []*restoredPlacement
	notices      []restoredHistoryNotice
	rendered     bool
	paging       bool // Bind cumulative answers only after a page has its chronological position.
}

func (u *appServerUI) restorePaneContent(root appServerThreadInfo) error {
	u.restoring = &appServerActivityRestore{root: root, threads: make(map[string]appServerThreadInfo), cursors: make(map[string]bool)}
	u.restoreDiffThread(root, true)
	u.status = "Restoring roster and Activity…"
	return u.restoreList("")
}

func (u *appServerUI) restoreDiffThread(info appServerThreadInfo, root bool) {
	if u.shell == nil || u.shell.auto == nil {
		return
	}
	u.shell.auto.includeThread(info.Cwd, info.ID, root)
	if root {
		u.shell.diff.workspace = info.Cwd
	}
}

func (u *appServerUI) restoreList(cursor string) error {
	if u.restoring.pages >= 8 {
		u.restoreContentNotice("Roster pagination exceeded the restoration limit.")
		return u.readRestoredChildren()
	}
	params := map[string]any{
		"ancestorThreadId": u.thread, "modelProviders": []string{},
		"sourceKinds": []string{"subAgentThreadSpawn"}, "limit": 100,
		"archived": u.restoring.archived,
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	return u.request("thread/list", params)
}

func (u *appServerUI) restoreContentNotice(message string) {
	u.restoreHistoryNotice(message, true)
}

func (u *appServerUI) restoreHistoryNotice(message string, incomplete bool) {
	if incomplete {
		u.agents.status = "History incomplete"
	}
	if r := u.restoring; r != nil && !r.rendered {
		r.notices = append(r.notices, restoredHistoryNotice{message, incomplete}) // Shown after Main's history.
		return
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: message, Observed: time.Now()}}})
	// Notices advance Main independently of the Activity sequence.
	u.session.seq = max(u.session.seq, u.view.lastSeq)
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
		r.next++
		return u.readNextRestoredChild()
	}
	if method == "thread/list" {
		r.pages++
		var result struct {
			Data       []appServerThreadInfo `json:"data"`
			NextCursor string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			u.restoreContentNotice("Invalid roster history: " + err.Error())
			return u.readRestoredChildren()
		}
		for _, info := range result.Data {
			if info.ID == "" || info.ID == u.thread || r.threads[info.ID].ID != "" {
				continue
			}
			if len(r.order) == appServerRestoreThreadLimit {
				u.restoreContentNotice("Activity restoration is limited to 128 child threads; additional history was not loaded.")
				return u.readRestoredChildren()
			}
			r.threads[info.ID] = info
			r.order = append(r.order, info.ID)
		}
		if result.NextCursor != "" {
			if r.cursors[result.NextCursor] {
				u.restoreContentNotice("Roster history returned a repeated pagination cursor.")
				return u.readRestoredChildren()
			}
			r.cursors[result.NextCursor] = true
			return u.restoreList(result.NextCursor)
		}
		if !r.archived {
			r.archived, r.cursors = true, make(map[string]bool)
			return u.restoreList("")
		}
		return u.readRestoredChildren()
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		u.restoreContentNotice("Invalid Activity history: " + err.Error())
	} else if result.Thread.ID != r.order[r.next] {
		u.restoreContentNotice("Activity history returned a different thread identity.")
	} else if result.Thread.HistoryMode == "paginated" {
		return u.startChildHistory(result.Thread)
	} else {
		u.restoreActivityThread(result.Thread)
	}
	r.next++
	return u.readNextRestoredChild()
}

func (u *appServerUI) readRestoredChildren() error {
	r := u.restoring
	r.listed = true
	// Register names before rendering cross-agent assignments and messages.
	for _, id := range r.order {
		info := r.threads[id]
		u.session.registerThread(info)
		u.restoreDiffThread(info, false)
		agent := u.session.agent(u.session.paths[id])
		agent.Started, agent.LastResponse = restoredAgentTimes(info)
		agent.WorkTimer = restoredAgentWork(info)
	}
	u.readRestoredRollouts()
	for _, turn := range r.root.Turns {
		for _, item := range turn.Items {
			if item.Type == "collabAgentToolCall" {
				item.SenderThreadID = u.thread
				entries := u.restoredCollab(item, historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, r.root.UpdatedAt)))
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
	infos := []appServerThreadInfo{r.root}
	for _, id := range r.order {
		infos = append(infos, r.threads[id])
	}
	for _, info := range infos {
		agent := u.session.path(info.ID)
		rollout := readRestoredRollout(info, agent)
		r.itemAt[info.ID] = rollout.itemAt
		if info.ID == r.root.ID {
			r.journalTurns = rollout.turnAt
		}
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
	if r.next < len(r.order) {
		u.status = fmt.Sprintf("Restoring Activity %d/%d…", r.next+1, len(r.order))
		return u.request("thread/read", map[string]any{"threadId": r.order[r.next], "includeTurns": r.threads[r.order[r.next]].HistoryMode != "paginated"})
	}
	if !r.rendered {
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
		r.root.Turns, r.main, r.rendered = nil, nil, true
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
	if r == nil || !r.listed || r.next < len(r.order) {
		return nil
	}
	if u.shell != nil && u.shell.auto != nil && u.shell.diffFailure == "" {
		a := u.shell.auto
		a.mu.Lock()
		scope := cloneLiveDiffScope(a.scope)
		a.mu.Unlock()
		for workspace, threads := range scope.Workspaces {
			for thread := range threads {
				if !u.shell.diff.scope.Workspaces[workspace][thread] {
					u.status = "Restoring saved Diff…"
					return nil
				}
			}
		}
	}
	u.restoring = nil
	u.updateHistoryHint()
	u.status = "Ready"
	if u.turn != "" {
		u.status = "Working"
	}
	return u.replayResumePending()
}

func (u *appServerUI) restoredCollab(item appServerItem, observed time.Time) []activityPaneEntry {
	if item.Status == "completed" {
		return u.session.collab(item, item.ID, observed)
	}
	var entries []activityPaneEntry
	for _, receiver := range item.ReceiverThreadIDs {
		entries = append(entries, activityPaneEntry{Seq: u.session.next(), Agent: u.session.path(receiver), Kind: "error", Observed: observed,
			Text:   "Historical " + item.Tool + ": " + cmp.Or(item.Status, "unknown status") + "; delivery not confirmed",
			native: &liveActivityNativeItem{thread: item.SenderThreadID, turn: "collaboration", item: item.ID + "\x00" + receiver, phase: "item/completed"}})
	}
	return entries
}

func historyTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func (u *appServerUI) applyRestoredActivity(entries []activityPaneEntry) {
	for i := range entries {
		u.annotateChildCompletion(&entries[i])
		entries[i].Agent = paneActivityAgent(entries[i])
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: entries, Agents: slices.Clone(u.session.agents)})
	for _, entry := range entries {
		if entry.Kind == "final" && (u.restoring == nil || !u.restoring.paging) {
			u.agents.linkChildAnswers(u.agents.entrySeq(entry))
		}
	}
	u.applyCapturedEdits()
}

func restoredAgentTimes(info appServerThreadInfo) (start, last time.Time) {
	for _, turn := range info.Turns {
		if at := historyTime(turn.StartedAt); !at.IsZero() && (start.IsZero() || at.Before(start)) {
			start = at
		}
		if at := historyTime(turn.CompletedAt); at.After(last) {
			last = at
		}
	}
	return start, last
}

// Restore only closed host intervals. Missing completion timing cannot prove
// work duration, and an old in-progress turn must not revive a live clock.
func restoredAgentWork(info appServerThreadInfo) (timer activeWorkTimer) {
	for _, turn := range info.Turns {
		start, end := historyTime(turn.StartedAt), historyTime(turn.CompletedAt)
		if start.IsZero() || end.IsZero() || end.Before(start) {
			continue
		}
		timer.Known = true
		timer.ElapsedNS += int64(end.Sub(start))
	}
	return timer
}

// Observational history never opens previews, resumes children, or fabricates
// a live response. Only a subsequent live notification can mark an agent busy.
func (u *appServerUI) restoreActivityThread(info appServerThreadInfo) {
	previousSegments := u.restoredSegments
	defer func() { u.restoredSegments = previousSegments }()
	s := &u.session
	name := s.paths[info.ID]
	agent := s.agent(name)
	restoreContextUsage(agent, info)
	u.restoreUsage(info)
	agent.Started, agent.LastResponse = restoredAgentTimes(info)
	agent.WorkTimer = restoredAgentWork(info)
	agent.Turns, agent.Responding = uint64(len(info.Turns)), false
	u.observeCost(info.ID, agent)
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(s.agents)})
	var r *appServerActivityRestore
	var itemAt map[string]time.Time
	byTurn := make(map[int][]*restoredPlacement)
	if r = u.restoring; r != nil {
		itemAt = r.itemAt[info.ID]
		for _, p := range r.pane[name] {
			index := restoredTurnFor(info.Turns, itemAt, p.at)
			if p.turn != "" {
				index = slices.IndexFunc(info.Turns, func(turn appServerHistoryTurn) bool { return turn.ID == p.turn })
			}
			if index >= 0 {
				byTurn[index] = append(byTurn[index], p)
			} else if main, ok := mainActivityEntry(p.entry); ok && p.turn == "" {
				r.main = append(r.main, &restoredPlacement{entry: main, at: p.at})
			}
		}
		delete(r.pane, name)
	}
	for index, turn := range info.Turns {
		u.restoredSegments = u.prepareCommandSegments(info.ID, u.session.cwd, turn)
		observed := historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt, info.UpdatedAt))
		var entries []activityPaneEntry
		var times []time.Time // When each entry happened, to place Main's copy.
		place := func(slot []*restoredPlacement) {
			for _, p := range slot {
				entry := p.entry
				entry.Seq = s.next()
				entries, times = append(entries, entry), append(times, p.at)
			}
		}
		slots := placeRestored(turn, itemAt, byTurn[index])
		place(slots[0])
		lastMessage := -1
		for i, item := range turn.Items {
			first := len(entries)
			at, ok := itemAt[item.ID]
			if !ok {
				at = observed
			}
			u.restoreActivityItem(info, turn, item, name, at, &entries, &lastMessage)
			for range entries[first:] {
				times = append(times, at)
			}
			place(slots[i+1])
		}
		agent = s.agent(name)
		agent.Final = turn.Status == "completed"
		if agent.Final && lastMessage >= 0 && !turn.olderPage {
			entries[lastMessage].Kind = "final"
		}
		if !turn.olderPage && (turn.Status == "failed" || turn.Status == "interrupted" || turn.Status == "inProgress") {
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

func (u *appServerUI) restoreActivityItem(info appServerThreadInfo, turn appServerHistoryTurn, item appServerItem, name string, observed time.Time, entries *[]activityPaneEntry, lastMessage *int) {
	if u.internalJournalCommand(info.ID, item) {
		return
	}
	s := &u.session
	entry := activityPaneEntry{Seq: s.next(), Agent: name, Observed: observed, CallID: item.ID,
		native: &liveActivityNativeItem{thread: info.ID, turn: turn.ID, item: item.ID, phase: "item/completed", searchResults: appServerSearchResults(item)}}
	progressPhase := appServerHistoryProgressPhase(item, turn.Status)
	item = u.waitItem(item, info.ID, turn.ID, item.ID, false)
	if text, wait, handled := u.progress(item, progressPhase, info.ID, turn.ID); handled {
		if text != "" {
			entry.Kind, entry.Text = "progress", text
			entry.native.wait = wait
			entry.native.phase = progressPhase
			*entries = append(*entries, u.progressEntry(entry))
		}
		return
	}
	switch item.Type {
	case "reasoning":
		entry.native.collapsed = true
		entry.Kind, entry.Text = "reasoning", strings.Join(item.Summary, "\n\n")
		if strings.TrimSpace(entry.Text) != "" {
			*entries = append(*entries, entry)
		}
	case "imageView":
		entry.Kind, entry.Text = "tool", "View "+commentaryCode(pathdisplay.ForWorkspace(info.Cwd, item.Path))
		*entries = append(*entries, entry)
	case "commandExecution", "fileChange", "webSearch":
		entry.Kind, entry.Text = "tool", appServerToolText(item, info.Cwd)
		entry.native.command, entry.native.duration = item.Command, appServerDuration(item)
		entry.native.workdir = appServerCommandWorkdir(item, info.Cwd)
		if item.Type == "fileChange" {
			entry.Text = appServerEditText(item, info.Cwd)
			entry.native.editPages = appServerEditPages(item, info.Cwd, entry.native.phase)
		}
		s.retainOutput(entry.native, item)
		appServerSucceededOutput(&entry, item, time.Time{})
		u.restoreCommandSegments(&entry, item, u.session.cwd)
		*entries = append(*entries, entry)
		if len(entry.native.segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := activityPaneEntry{Seq: s.next(), Agent: name, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID, Observed: observed}
			exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
			*entries = append(*entries, exit)
		}
	case "agentMessage":
		if item.Delivery == "async" && len(item.Questions) > 0 {
			return
		}
		entry.Kind, entry.Text = "text", item.Text
		if item.Phase == "final_answer" || item.Phase == "finalAnswer" {
			entry.Kind = "final"
		}
		*lastMessage = len(*entries)
		*entries = append(*entries, entry)
	case "collabAgentToolCall":
		*entries = append(*entries, u.restoredCollab(item, observed)...)
	}
}
