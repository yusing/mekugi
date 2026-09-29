package router

import (
	json "encoding/json/v2"
	"slices"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Only commands implemented by the native shell belong in this catalog.
var nativeCommands = []composerChoice{
	{name: "/model", description: "Choose the model"},
	{name: "/reasoning", description: "Choose the reasoning effort"},
	{name: "/effort", description: "Choose the reasoning effort"},
	{name: "/tier", description: "Choose the service tier"},
	{name: "/skills", description: "List or manage skills"},
	{name: "/btw", description: "Ask a side question without changing Main"},
	{name: "/status", description: "Show session settings and usage limits"},
	{name: "/copy", description: "Copy the last response or part of it"},
	{name: "/compact", description: "Compact context, or queue compaction while busy"},
	{name: "/clear", description: "Clear the transcript and start a new session"},
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
		return u.flushInput()
	}
	if u.turn != "" || u.starting || u.compactRequest || u.submission.text != "" || u.settingsPending || u.shellPending.text != "" || len(u.unsent)+len(u.queued) > 0 || u.reset.active() {
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
		u.retiredThreads = make(map[string]bool)
	}
	u.retiredThreads[u.thread] = true
	for thread := range u.session.paths {
		u.retiredThreads[thread] = true
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
	if err := u.request("thread/unsubscribe", map[string]any{"threadId": u.thread}); err != nil {
		return err
	}
	u.picker = composerPicker{}
	u.resumeThread, u.resumeCwd, u.resumePending = "", "", nil
	u.compacting, u.polling = nil, nil
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
	if u.retiredThreads[p.Thread.ParentThreadID] {
		u.retiredThreads[p.Thread.ID] = true
	}
	return u.retiredThreads[p.ThreadID] || u.retiredThreads[p.Thread.ID]
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
