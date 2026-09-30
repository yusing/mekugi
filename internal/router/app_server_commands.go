package router

import (
	json "encoding/json/v2"
	"slices"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// Only commands implemented by the native shell belong in this catalog.
var nativeCommands = []composerChoice{
	{name: "/model", description: "Choose the model"},
	{name: "/reasoning", description: "Choose the reasoning effort"},
	{name: "/effort", description: "Choose the reasoning effort"},
	{name: "/live", description: "Show or hide Live edits for this session"},
	{name: "/tier", description: "Choose the service tier"},
	{name: "/skills", description: "List or manage skills"},
	{name: "/btw", description: "Ask a side question without changing Main"},
	{name: "/session", description: "Show request metrics, cache and transport"},
	{name: "/status", description: "Show session settings and usage limits"},
	{name: "/copy", description: "Copy the last response or part of it"},
	{name: "/resume", description: "Resume a saved session"},
	{name: "/compact", description: "Compact context, or queue compaction while busy"},
	{name: "/clear", description: "Clear the transcript and start a new session"},
	{name: "/lock", description: "Prevent accidental keyboard interruption"},
	{name: "/unlock", description: "Allow keyboard interruption again"},
	{name: "/quit", description: "Quit the session"},
}

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:268:310 and
// codex-rs/tui/src/app/event_dispatch.rs:385:401@68e1a421.
// Clear starts a fresh host thread.
// Unlike stock's busy rejection, compact waits locally: the RPC replaces an
// active host task, so it must never be sent as a turn/steer text payload.
func (u *appServerUI) sessionCommand(command string) error {
	if u.thread == "" || u.restoring != nil || u.clearing {
		u.setNotice("Wait for the session to be ready", false)
		return nil
	}
	if command == "/compact" {
		u.unsent = append(u.unsent, u.takeDraft())
		u.unsent[len(u.unsent)-1].text = "/compact"
		u.unsent[len(u.unsent)-1].continueTask = u.turn != "" || u.starting
		return u.flushInput()
	}
	if u.sessionBusy() {
		u.setNotice("/clear is disabled while a task is in progress", true)
		return nil
	}
	u.takeDraft()
	u.clearing = true
	u.status = "Starting new session…"
	params := map[string]any{"sessionStartSource": "clear", "approvalPolicy": "never", "sandbox": "danger-full-access"}
	if u.session.cwd != "" {
		params["cwd"] = u.session.cwd
	}
	if u.model != "" {
		params["model"] = u.model
	}
	if u.statusConfig.Provider != "" {
		params["modelProvider"] = u.statusConfig.Provider
	}
	if u.reasoningEffort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": u.reasoningEffort}
	}
	if u.serviceTier != "" {
		params["serviceTier"] = u.serviceTier
	}
	if err := u.request("thread/start", params); err != nil {
		u.clearing = false
		return err
	}
	return nil
}

// sessionBusy reports work that leaving the current thread would strand.
func (u *appServerUI) sessionBusy() bool {
	return u.turn != "" || u.starting || u.compactRequest || u.submission.text != "" || u.settingsPending || u.shellPending.text != "" || len(u.unsent)+len(u.queued) > 0 || u.reset.active()
}

// A queued compact is a local action, not Main's running turn. Return waiting
// input for retry without interrupting that turn or an in-flight submission.
func (u *appServerUI) cancelQueuedCompact() bool {
	if u.manualCompact || u.compactRequest {
		return false
	}
	isCompact := func(d composerDraft) bool { return d.text == "/compact" }
	if !slices.ContainsFunc(u.unsent, isCompact) && !slices.ContainsFunc(u.queued, isCompact) {
		return false
	}
	u.continueAfterCompact = false
	u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
	u.unsent, u.queued = nil, nil
	u.setNotice("Queued compaction cancelled · input restored · Main continues", false)
	return true
}

// Detach presentation only after the new host thread exists. Saved threads,
// journal evidence, and running children remain owned by Codex.
func (u *appServerUI) clearSessionPresentation() error {
	if err := u.closeBTW(); err != nil {
		return err
	}
	if u.panes != nil {
		u.paneError(u.panes.save(u.shell, time.Now(), true))
	}
	u.cancelPickerScan()
	if u.retiredThreads == nil {
		u.retiredThreads = make(map[string]string)
	}
	u.retiredThreads[u.thread] = u.thread
	for thread := range u.session.paths {
		u.retiredThreads[thread] = u.thread
	}
	if u.proxy != nil {
		u.proxy.journals.detachNative(u.journal)
		u.proxy.journals.detachNative(u.unscopedJournal)
	}
	u.journal, u.unscopedJournal, u.reset = nil, nil, nil
	u.journalView = nativeJournalView{}
	u.hideQuestions()
	u.questions = nativeQuestionDock{}
	if u.notifications != nil {
		clear(u.notifications.blocked)
	}
	u.statusPanel, u.statusReports = nil, nil
	u.requests = make(map[string]string) // Retire old-thread response correlation.
	u.childHistory, u.historyLoading = nil, nil
	if err := u.request("thread/unsubscribe", map[string]any{"threadId": u.thread}); err != nil {
		return err
	}
	u.picker = composerPicker{}
	u.resumeThread, u.resumeCwd = "", ""           // A switch keeps its buffered events for restoration.
	u.turn, u.compacting, u.polling = "", nil, nil // The left session stayed live until now.
	u.pendingStart = composerSubmission{}
	u.turnStarted = time.Time{}
	u.exitUsage = appServerTokenUsage{}
	u.alert, u.clearing = false, false
	u.setNotice("", false)
	u.session.outputs = activityui.Retention{}
	painter := u.view.painter
	*u.view = *newLiveActivityView()
	u.view.painter, u.view.conversation = painter, true
	if u.agents != nil {
		painter = u.agents.painter
		*u.agents = *newLiveActivityView()
		u.agents.painter, u.agents.mainView = painter, u.view
		u.agents.childrenOnly, u.agents.bare = true, true
	}
	if u.shell != nil {
		if u.shell.autoActivity {
			u.shell.journalOpen = true
		}
		u.activeChildren, u.shell.autoActivity = nil, false
		u.shell.selection, u.shell.output = nil, nil
		u.shell.focus, u.shell.drag = 0, 0
	}
	return nil
}

func (u *appServerUI) retiredSessionEvent(m appserver.Message) bool {
	if len(u.retiredThreads) == 0 || m.Method == "" {
		return false
	}
	var p struct {
		ThreadID string              `json:"threadId"`
		Thread   appServerThreadInfo `json:"thread"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return false
	}
	if root := u.retiredThreads[p.Thread.ParentThreadID]; root != "" {
		u.retiredThreads[p.Thread.ID] = root
	}
	return u.retiredThreads[p.ThreadID] != "" || u.retiredThreads[p.Thread.ID] != ""
}

// releaseRetired readmits a session the UI returns to, with its descendants.
// retiredThreads maps each retired thread to the session root it belonged to.
func (u *appServerUI) releaseRetired(root string) {
	delete(u.retiredThreads, root)
	for thread, owner := range u.retiredThreads {
		if owner == root {
			delete(u.retiredThreads, thread)
		}
	}
}

// resetDiffScope starts the saved Diff over for a resumed root thread; its
// restoration then includes only that thread and its descendants.
func (u *appServerUI) resetDiffScope() {
	if u.shell == nil || u.shell.auto == nil {
		return
	}
	u.shell.diff.resetScope()
	u.shell.liveDock, u.shell.diffFailure, u.shell.diffUnseen = diffview.PreviewPane{}, "", false
	u.shell.auto.resetScope()
}

func (u *appServerUI) filterCommands(query string) {
	p := &u.picker
	previous := ""
	if p.selected < len(p.choices) {
		previous = p.choices[p.selected].name
	}
	p.choices, p.loading, p.problem = nil, false, ""
	for _, command := range nativeCommands {
		if _, ok := pickerMatchScore(command.name[1:], query); ok {
			p.choices = append(p.choices, command)
		}
	}
	slices.SortStableFunc(p.choices, func(a, b composerChoice) int {
		aScore, _ := pickerMatchScore(a.name[1:], query)
		bScore, _ := pickerMatchScore(b.name[1:], query)
		return aScore - bScore
	})
	p.selected = max(0, slices.IndexFunc(p.choices, func(c composerChoice) bool { return c.name == previous }))
}
