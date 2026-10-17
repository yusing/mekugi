package router

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/conversation/completion"

	"slices"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
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
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait for the session to be ready", false)
		return nil
	}
	if command == "/compact" {
		return u.submitCompact(false)
	}
	if sessionBusy(&u.Live, u.attached.conversation) {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "/clear is disabled while a task is in progress", true)
		return nil
	}
	prepareDraft(&u.picker, u.shell, u.attached.cwd, u.skillEnvironment, u.Editor.Take())
	u.Replacement.Clear()
	u.attached.presentation.Status.SetText("Starting new session…")
	params := sessioncontrol.ThreadPermissions(map[string]any{"sessionStartSource": "clear"}, u.approvalMode)
	if u.attached.cwd != "" {
		params["cwd"] = u.attached.cwd
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
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait for the session to be ready", false)
		return nil
	}
	draft := prepareDraft(&u.picker, u.shell, u.attached.cwd, u.skillEnvironment, u.Editor.Take())
	draft.Text = "/compact"
	draft.Compact = conversationmodel.CompactIntent{WaitForIdle: queue, Continue: u.Turn != "" || u.Starting()}
	u.attached.conversation.Unsent = append(u.attached.conversation.Unsent, draft)
	return u.flushInput()
}

// sessionBusy reports work that leaving the current thread would strand.
func sessionBusy(control *sessioncontrol.Live, conversation *conversationmodel.Session) bool {
	return control.Busy(len(conversation.Unsent)+len(conversation.Queued) > 0)
}

// A queued compact is a local action, not Main's running turn. Return waiting
// input for retry without interrupting that turn or an in-flight submission.
func (u *appServerUI) cancelQueuedCompact() bool {
	isCompact := func(d conversationmodel.Input) bool { return d.Text == "/compact" }
	hasQueued := slices.ContainsFunc(u.attached.conversation.Unsent, isCompact) || slices.ContainsFunc(u.attached.conversation.Queued, isCompact)
	cancelled, interruptionIssued := u.Compaction.CancelQueued(hasQueued)
	if !cancelled {
		return false
	}
	restoreDrafts(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, slices.Concat(u.attached.conversation.Unsent, u.attached.conversation.Queued)...)
	u.attached.conversation.Unsent, u.attached.conversation.Queued = nil, nil
	if interruptionIssued {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Compaction cancelled · input restored · interruption already requested", false)
	} else {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Queued compaction cancelled · input restored · Main continues", false)
	}
	return true
}

// resetDiffScope starts the saved Diff over for a resumed root thread; its
// restoration then includes only that thread and its descendants.
func resetDiffScope(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), parent context.Context) {
	if review == nil {
		return
	}
	review.Replace(parent)
	if terminal != nil {
		if terminal.Auxiliary != nil {
			terminal.Auxiliary.Reset()
		}
		terminal.Live, terminal.DiffUnseen = previewview.PreviewPane{}, false
	}
	publishReview(attached, terminal, review, source, prepare)
}

func (p *composerPicker) filterCommands(query string) {
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
