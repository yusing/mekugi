package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

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
func sessionCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, restoring bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, provider string, approvalMode bool, command string) error {
	if control.Thread == "" || restoring || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return nil
	}
	if command == "/compact" {
		return submitCompact(attached, terminal, control, picker, editor, questions, approvals, clock, settings, tiers, restoring, owned, environment, hook, observations, parent, reviewOwner, receipts, false)
	}
	if sessionBusy(&control.Live, attached.conversation) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "/clear is disabled while a task is in progress", true)
		return nil
	}
	prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
	control.Replacement.Clear()
	attached.presentation.Status.SetText("Starting new session…")
	params := sessioncontrol.ThreadPermissions(map[string]any{"sessionStartSource": "clear"}, approvalMode)
	if attached.cwd != "" {
		params["cwd"] = attached.cwd
	}
	if settings.Model != "" {
		params["model"] = settings.Model
	}
	if provider != "" {
		params["modelProvider"] = provider
	}
	if settings.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": settings.Effort}
	}
	if settings.Tier != "" {
		params["serviceTier"] = settings.Tier
	}
	if err := control.Request("thread/start", params); err != nil {
		control.Replacement.Finish()
		return err
	}
	return nil
}

func submitCompact(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, restoring bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, queue bool) error {
	if control.Thread == "" || restoring || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return nil
	}
	draft := prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
	draft.Text = "/compact"
	draft.Compact = conversationmodel.CompactIntent{WaitForIdle: queue, Continue: control.Turn != "" || control.Starting()}
	attached.conversation.Unsent = append(attached.conversation.Unsent, draft)
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
}

// sessionBusy reports work that leaving the current thread would strand.
func sessionBusy(control *sessioncontrol.Live, conversation *conversationmodel.Session) bool {
	return control.Busy(len(conversation.Unsent)+len(conversation.Queued) > 0)
}

// A queued compact is a local action, not Main's running turn. Return waiting
// input for retry without interrupting that turn or an in-flight submission.
func cancelQueuedCompact(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time) bool {
	isCompact := func(d conversationmodel.Input) bool { return d.Text == "/compact" }
	hasQueued := slices.ContainsFunc(attached.conversation.Unsent, isCompact) || slices.ContainsFunc(attached.conversation.Queued, isCompact)
	cancelled, interruptionIssued := control.Compaction.CancelQueued(hasQueued)
	if !cancelled {
		return false
	}
	restoreDrafts(attached, terminal, questions, approvals, picker, control.Turn, clock, slices.Concat(attached.conversation.Unsent, attached.conversation.Queued)...)
	attached.conversation.Unsent, attached.conversation.Queued = nil, nil
	if interruptionIssued {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Compaction cancelled · input restored · interruption already requested", false)
	} else {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Queued compaction cancelled · input restored · Main continues", false)
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
