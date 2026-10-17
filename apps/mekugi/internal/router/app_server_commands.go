package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/completion"

	"slices"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// Only commands implemented by the native shell belong in this catalog.
var nativeCommands = []completion.Choice{
	{Name: "/model", Description: "Choose the model"},
	{Name: "/reasoning", Description: "Choose the reasoning effort"},
	{Name: "/effort", Description: "Choose the reasoning effort"},
	{Name: "/live", Description: "Show or hide Live edits for this session"},
	{Name: "/tier", Description: "Choose the service tier"},
	{Name: "/skills", Description: "List or manage skills"},
	{Name: "/btw", Description: "Ask a side question without changing Main"},
	{Name: "/ps", Description: "Show running processes"},
	{Name: "/session", Description: "Show request metrics, cache and transport"},
	{Name: "/title", Description: "Rename this session"},
	{Name: "/status", Description: "Show session settings and usage limits"},
	{Name: "/copy", Description: "Copy the last response or part of it"},
	{Name: "/resume", Description: "Resume a saved session"},
	{Name: "/archive", Description: "Archive this session"},
	{Name: "/compact", Description: "Compact context now; Tab queues while busy"},
	{Name: "/clear", Description: "Clear the transcript and start a new session"},
	{Name: "/lock", Description: "Prevent accidental keyboard interruption"},
	{Name: "/unlock", Description: "Allow keyboard interruption again"},
	{Name: "/quit", Description: "Quit the session"},
}

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:268:310 and
// codex-rs/tui/src/app/event_dispatch.rs:385:401@68e1a421.
// Clear starts a fresh host thread.
// Compact uses its native RPC after acknowledged interruption, never a
// turn/steer text payload. Tab can explicitly queue it instead.
func (u *appServerUI) sessionCommand(command string) error {
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return nil
	}
	if command == "/compact" {
		return u.submitCompact(false)
	}
	if u.sessionBusy() {
		u.setNotice("/clear is disabled while a task is in progress", true)
		return nil
	}
	u.takeDraft()
	u.Replacement.Clear()
	u.sessionPresentation().Status.SetText("Starting new session…")
	params := sessioncontrol.ThreadPermissions(map[string]any{"sessionStartSource": "clear"}, u.approvalMode)
	if u.cwd != "" {
		params["cwd"] = u.cwd
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
	if err := u.Request("thread/start", params); err != nil {
		u.Replacement.Finish()
		return err
	}
	return nil
}

func (u *appServerUI) submitCompact(queue bool) error {
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return nil
	}
	draft := u.takeDraft()
	draft.Text = "/compact"
	draft.Compact = conversationmodel.CompactIntent{WaitForIdle: queue, Continue: u.Turn != "" || u.Starting()}
	u.conversation().Unsent = append(u.conversation().Unsent, draft)
	return u.flushInput()
}

// sessionBusy reports work that leaving the current thread would strand.
func (u *appServerUI) sessionBusy() bool {
	return u.Busy(len(u.conversation().Unsent)+len(u.conversation().Queued) > 0)
}

// A queued compact is a local action, not Main's running turn. Return waiting
// input for retry without interrupting that turn or an in-flight submission.
func (u *appServerUI) cancelQueuedCompact() bool {
	isCompact := func(d conversationmodel.Input) bool { return d.Text == "/compact" }
	hasQueued := slices.ContainsFunc(u.conversation().Unsent, isCompact) || slices.ContainsFunc(u.conversation().Queued, isCompact)
	cancelled, interruptionIssued := u.Compaction.CancelQueued(hasQueued)
	if !cancelled {
		return false
	}
	u.restoreDrafts(slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)
	u.conversation().Unsent, u.conversation().Queued = nil, nil
	if interruptionIssued {
		u.setNotice("Compaction cancelled · input restored · interruption already requested", false)
	} else {
		u.setNotice("Queued compaction cancelled · input restored · Main continues", false)
	}
	return true
}

// Detach presentation only after the new host thread exists. Saved threads,
// change evidence, and running children remain owned by Codex.
func (u *appServerUI) clearSessionPresentation() error {
	if err := u.closeBTW(); err != nil {
		return err
	}
	if u.panes != nil {
		u.paneError(u.panes.save(u.shell.Shell.Preferences(&u.mainView), time.Now(), true))
	}
	u.cancelPickerScan()
	roster := u.session.Roster()
	retired := make([]string, 0, len(roster))
	for _, agent := range roster {
		retired = append(retired, agent.Thread)
	}
	u.RetireThreads(u.Thread, retired...)
	u.hideQuestions()
	retainedQuestions := u.questionCalls()
	if u.notifications != nil {
		clear(u.notifications.blocked)
	}
	u.statusPanel = nil
	u.sessionPresentation().ClosePanel()
	u.Requests.Retire("main") // Retire old-thread response correlation.
	if err := u.Request("thread/unsubscribe", map[string]any{"threadId": u.Thread}); err != nil {
		return err
	}
	u.picker = composerPicker{}
	u.exitUsage = binding.TokenUsage{}
	u.feedback = noticepresentation.Model{}
	u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	for key, run := range u.session.Commands() {
		if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
			u.execTrack.ReleaseOutput(key)
		}
	}
	waiting := u.conversation()
	u.session.Retire()
	u.session = sessionpresentation.Attachment{}
	u.conversation().CarryInput(waiting)
	u.bindConversation()
	u.questions = nativeQuestionDock{Questions: &u.conversation().Questions}
	for _, c := range retainedQuestions {
		if c.Sudo != nil && !c.Resolved {
			u.addQuestions(c, true)
		}
	}
	painter := u.view.Painter
	u.view = activityui.NewView()
	u.view.Painter, u.view.Conversation = painter, true
	if u.agents != nil {
		painter = u.agents.Painter
		u.agents = u.terminalViews.NewThreadView()
		u.agents.Painter = painter
		u.agents.ChildrenOnly, u.agents.Bare = true, true
	}
	u.source, u.childSource = nil, nil
	u.attachPresentation()
	if u.shell != nil {
		u.shell.Agents = u.agents
		u.shell.Selection = nil
		u.detailFiles.closeOutput(&u.session, u.shell)
		u.shell.Focus = 0
		u.shell.StopDrag()
	}
	return nil
}

// resetDiffScope starts the saved Diff over for a resumed root thread; its
// restoration then includes only that thread and its descendants.
func (u *appServerUI) resetDiffScope() {
	if u.review == nil {
		return
	}
	u.review.Replace(u.ctx)
	if u.shell != nil {
		if u.shell.Auxiliary != nil {
			u.shell.Auxiliary.Reset()
		}
		u.shell.Live, u.shell.DiffUnseen = previewview.PreviewPane{}, false
	}
	u.publishReview()
}

func (u *appServerUI) filterCommands(query string) {
	p := &u.picker
	p.Choices, p.Loading, p.Problem = nil, false, ""
	for _, command := range nativeCommands {
		if _, ok := completion.MatchScore(command.Name[1:], query); ok {
			p.Choices = append(p.Choices, command)
		}
	}
	slices.SortStableFunc(p.Choices, func(a, b completion.Choice) int {
		aScore, _ := completion.MatchScore(a.Name[1:], query)
		bScore, _ := completion.MatchScore(b.Name[1:], query)
		return aScore - bScore
	})
}
