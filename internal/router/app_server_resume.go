package router

import (
	"cmp"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func (u *appServerUI) requestResume(thread string) error {
	u.status = "Resuming thread…"
	_, err := u.requestAs("thread/read", "resume/settings", map[string]any{"threadId": thread, "includeTurns": false})
	return err
}

func (u *appServerUI) resumeSettingsResponse(m appserver.Message) error {
	fail := func(message string) error {
		if u.replacement.target != "" {
			return u.resumeSessionFailed(message)
		}
		return fmt.Errorf("read resume settings: %s", message)
	}
	if m.Error != nil {
		return fail(m.Error.Message)
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		return fail(err.Error())
	}
	thread := cmp.Or(u.replacement.target, u.resumeThread)
	if result.Thread.ID != thread {
		return fail("thread/read returned a different thread identity")
	}
	// Codex skips persisted model/effort when the routing provider is explicit.
	// Read settings before resume can write its new defaults into the rollout.
	config := maps.Clone(u.resumeConfig)
	if config == nil {
		config = make(map[string]any)
	}
	var retained nativeAppliedSettings
	var recovery nativeResumeEvidence
	var newerThan time.Time
	if u.panes != nil {
		state, err := u.panes.read(result.Thread.Cwd, thread)
		if err != nil {
			u.paneError(err)
		} else {
			retained = state.Settings
			newerThan = retained.Observed
			if state.ResumeEvidence.Model != "" && state.ResumeObserved.After(newerThan) {
				recovery = state.ResumeEvidence
				newerThan = state.ResumeObserved
			}
		}
	}
	saved := retained.config()
	if recovery.Model != "" {
		saved = recovery.config()
	}
	if saved == nil {
		saved = make(map[string]any)
	}
	maps.Copy(saved, readResumeSettings(result.Thread, newerThan))
	u.resumeEvidence.Model, _ = saved["model"].(string)
	u.resumeEvidence.Effort, _ = saved["model_reasoning_effort"].(string)
	u.resumeEvidence.Tier, _ = saved["service_tier"].(string)
	_, u.resumeEvidence.TierKnown = saved["service_tier"]
	u.resumeNotice = ""
	if len(saved) == 0 {
		u.resumeNotice = "Saved model settings unavailable; using Codex defaults and explicit flags"
		u.setNotice(u.resumeNotice, true)
	}
	params := u.threadPermissions(map[string]any{"threadId": thread, "config": config, "modelProvider": config["model_provider"]})
	u.resumePendingEffort = false
	for field, value := range saved {
		if _, explicit := config[field]; explicit {
			continue
		}
		if field == "model_reasoning_effort" && value == nil {
			// TOML config cannot express null. Clear via the host settings API
			// before any resumed input is accepted instead.
			u.resumePendingEffort = true
		} else {
			config[field] = value
		}
	}
	if tier, ok := config["service_tier"]; ok {
		params["serviceTier"] = tier
		delete(config, "service_tier")
	}
	return u.request("thread/resume", params)
}

// resumableThreads lists the sessions `resume --last` and the picker choose
// from: interactive and app-server sessions from every provider. Exec runs and
// spawned agents are not resumable conversations.
func resumableThreads(limit int) map[string]any {
	return map[string]any{
		"limit": limit, "sortKey": "updated_at", "archived": false,
		"modelProviders": []string{}, "sourceKinds": []string{"cli", "vscode", "appServer"},
	}
}

// holdResumeEvent decides whether an event waits for a resuming thread's
// history. During an in-session switch the current session stays live, so
// only events for other threads wait. Streaming deltas from the resumed
// thread's descendants are dropped rather than kept: their completed items
// carry the full content, and busy children must not exhaust the bounded
// buffer.
func (u *appServerUI) holdResumeEvent(m appserver.Message, resuming bool) (hold, keep bool) {
	var p struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parentThreadId"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(m.Params, &p)
	thread := cmp.Or(p.ThreadID, p.Thread.ID)
	if !resuming && (thread == "" || u.session.paths[thread] != "" || u.session.paths[p.Thread.ParentThreadID] != "") {
		return false, false
	}
	root := cmp.Or(u.replacement.target, u.resumeThread)
	delta := strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta")
	return true, !delta || thread == "" || thread == root
}

// replayResumePending delivers events buffered while a thread was pending.
func (u *appServerUI) replayResumePending() error {
	pending := u.takeEvents()
	for _, event := range pending {
		if err := u.message(event); err != nil {
			return err
		}
	}
	return nil
}

func (u *appServerUI) resumeLastResponse(m appserver.Message) error {
	if m.Error != nil {
		return fmt.Errorf("find latest thread: %s", m.Error.Message)
	}
	var result struct {
		Data []appServerThreadInfo `json:"data"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		return fmt.Errorf("find latest thread: %w", err)
	}
	if len(result.Data) == 0 {
		return fmt.Errorf("no resumable thread found for %s", u.resumeCwd)
	}
	if result.Data[0].ID == "" {
		return fmt.Errorf("thread/list returned no thread identity")
	}
	u.resumeThread = result.Data[0].ID
	return u.requestResume(u.resumeThread)
}

// Codex merges saved model metadata after loading process CLI configuration.
// Forward explicit model settings on resume as well so that invocation-local
// overrides retain their meaning. Other config keeps its app-server owner.
func appServerResumeConfig(args, resumeArgv []string) map[string]any {
	config := make(map[string]any)
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" || i+1 == len(args) {
			continue
		}
		i++
		key, value, ok := strings.Cut(args[i], "=")
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		if !ok || (key != "model" && key != "model_provider" && key != "model_reasoning_effort" && key != "service_tier") {
			continue
		}
		var parsed map[string]any
		if _, err := toml.Decode("value = "+value, &parsed); err == nil {
			config[key] = parsed["value"]
		} else {
			// Codex accepts bare strings when a CLI value is not valid TOML.
			config[key] = strings.TrimSpace(value)
		}
	}
	if len(resumeArgv) > 0 {
		_, userArgs, err := SplitCommand(resumeArgv[1:])
		if err == nil && !HasModelOverride(userArgs) {
			// Startup defaults are not explicit resume overrides. Retain the
			// original invocation's selection, not launcher-generated config.
			delete(config, "model")
		}
	}
	return config
}

type appServerHistoryTurn struct {
	olderPage   bool            // An older item page must not repeat status or promote commentary to final.
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Items       []appServerItem `json:"items"`
	StartedAt   int64           `json:"startedAt"`
	CompletedAt int64           `json:"completedAt"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// History is presentation evidence, not a stream of live lifecycle events.
// In particular, old edits and collaboration calls must not revive previews,
// processes, child agents, or journal delivery receipts. restoreMainHistory
// also places restored evidence the host's history omits among Main's items: at its rollout anchor, or by time for evidence from
// another thread. itemAt holds the root's retained item completion times.
func (u *appServerUI) restoreMainHistory(turns []appServerHistoryTurn, placements []*restoredPlacement, itemAt map[string]time.Time) {
	previousSegments := u.restoredSegments
	defer func() { u.restoredSegments = previousSegments }()
	placements = append(placements, u.restoredJournalPlacements(turns, itemAt)...)
	byTurn := make(map[int][]*restoredPlacement)
	for _, p := range placements {
		index := restoredTurnFor(turns, itemAt, p.at)
		if p.turn != "" {
			// An anchor in a turn the host no longer shows has no position.
			index = slices.IndexFunc(turns, func(turn appServerHistoryTurn) bool { return turn.ID == p.turn })
		}
		if index >= 0 {
			byTurn[index] = append(byTurn[index], p)
		}
	}
	for index, turn := range turns {
		u.restoredSegments = u.prepareCommandSegments(u.thread, u.session.cwd, turn)
		slots := placeRestored(turn, itemAt, byTurn[index])
		u.applyRestoredMain(slots[0])
		for i, item := range turn.Items {
			before := u.view.lastSeq
			u.restoreHistoryItem(turn, item)
			at := cmp.Or(itemAt[item.ID], historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt)))
			if !at.IsZero() {
				for j := len(u.view.entries) - 1; j >= 0 && u.view.entries[j].Seq > before; j-- {
					u.view.mutateEntry(j, func(record *liveActivityRecord) { record.Observed = at })
				}
			}
			u.applyRestoredMain(slots[i+1])
		}
		if turn.Status == "failed" {
			// Live, the failure only reaches the status line, which resuming replaces.
			text := "Turn failed"
			if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
				text += ": " + turn.Error.Message
			}
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "error", Text: text,
				Observed: historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt))}}})
		}
		if turn.Status == "inProgress" {
			u.turn, u.status, u.turnStarted = turn.ID, "Working", time.Now()
		}
	}
	root := u.session.agent("/root")
	root.Started, root.LastResponse = restoredAgentTimes(appServerThreadInfo{Turns: turns})
	root.WorkTimer = restoredAgentWork(appServerThreadInfo{Turns: turns})
	root.Turns = uint64(len(turns))
	root.Responding = u.turn != ""
	// Keep future activity sequence numbers ahead of the hydrated transcript.
	u.session.seq = max(u.session.seq, u.view.lastSeq)
	u.applyActivity(nil, u.session.agents)
}

func (u *appServerUI) applyRestoredMain(placements []*restoredPlacement) {
	for _, p := range placements {
		entry := p.entry
		if p.link != nil && mainActivityLinked(entry) {
			entry.activitySeq = *p.link
		}
		if entry.Observed.IsZero() {
			entry.Observed = p.at
		}
		u.applyMainActivity(entry)
	}
}

func (u *appServerUI) restoreHistoryItem(turn appServerHistoryTurn, item appServerItem) {
	if u.observeQuestionItem(u.thread, turn.ID, item, true) {
		return
	}
	if item.Type == "userMessage" {
		u.commitQuestionReplies(item, turn.ID)
	}
	item = u.waitItem(item, u.thread, turn.ID, item.ID, false)
	if item.Type == "userMessage" {
		var content []composerUserContent
		if json.Unmarshal(item.Content, &content) == nil {
			var text strings.Builder
			var attached []string
			var spans []activityui.TextSpan
			for _, part := range content {
				if part.Type == "text" {
					if frames, ok := decodeFileAttachments(part.Text); ok {
						attached = append(attached, frames...)
						continue
					}
					for _, span := range composerElementSpans(part.Text, part.Elements) {
						span.Start += text.Len()
						span.End += text.Len()
						spans = append(spans, span)
					}
					text.WriteString(part.Text)
				}
			}
			draft := composerDraft{text: text.String()}
			draft.selections = restoredSelections(draft.text, attached)
			draft.skills = unambiguousSkillBindings(draft.text, composerContentSkills(content), spans)
			u.rememberInput(draft)
		}
	}
	method := "item/completed"
	if turn.Status == "inProgress" && (item.Type == "agentMessage" || item.Status == "inProgress") {
		method = "item/started"
	}
	if _, _, progress := appServerProgress(item, method); progress {
		method = appServerHistoryProgressPhase(item, turn.Status)
		if text, wait, _ := u.progress(item, method, u.thread, turn.ID); text != "" {
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{u.progressEntry(activityPaneEntry{
				Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "progress", Text: text, Observed: time.Now(),
				native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method, wait: wait},
			})}})
		}
		return
	}
	switch item.Type {
	case "imageView":
		entry := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "tool", Text: "View " + commentaryCode(pathdisplay.ForWorkspace(u.session.cwd, item.Path)), CallID: item.ID, Observed: time.Now(),
			native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method, searchResults: appServerSearchResults(item)}}
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	case "commandExecution", "fileChange", "webSearch":
		item, operation, shown := u.journalTransport(u.thread, item)
		if !shown {
			return
		}
		text := appServerToolText(item, u.session.cwd)
		if item.Type == "fileChange" {
			text = appServerEditText(item, u.session.cwd)
		}
		entry := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "tool", Text: text, CallID: item.ID, Observed: time.Now(),
			native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method, operation: operation, command: item.Command, commandCwd: appServerCommandDirectory(item, u.session.cwd), status: item.Status, duration: appServerDuration(item), searchResults: appServerSearchResults(item), workdir: appServerCommandWorkdir(item, u.session.cwd)}}
		if item.Type == "fileChange" {
			entry.native.editPages = appServerEditPages(item, u.session.cwd, method)
		}
		u.session.retainOutput(entry.native, item)
		appServerSucceededOutput(&entry, item, time.Time{})
		u.restoreCommandSegments(&entry, item, u.session.cwd)
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		if len(entry.native.segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID}
			exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{exit}})
		}
	default:
		if item.Type == "agentMessage" {
			item.replacesItems = u.proxy.commentaryReplacementItems(u.ctx, u.session.cwd, u.thread, turn.ID, item.ID)
		}
		u.view.applyAppServerItem(false, u.session.cwd, u.thread, u.thread, turn.ID, item.ID, method, "", item)
	}
}
