package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/conversation/completion"

	"slices"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Only commands implemented by the native shell belong in this catalog.
var nativeCommands = []completion.Choice{
	{Name: "/model", HideRemote: true, Description: "Choose the model"},
	{Name: "/permissions", Description: "Choose Main's permission profile for subsequent turns"},
	{Name: "/reasoning", HideRemote: true, Description: "Choose the reasoning effort"},
	{Name: "/effort", HideRemote: true, Description: "Choose the reasoning effort"},
	{Name: "/live", Description: "Show or hide Live edits for this session"},
	{Name: "/tier", HideRemote: true, Description: "Choose the service tier"},
	{Name: "/skills", Description: "List or manage skills"},
	{Name: "/presets", Description: "Save and recall numbered prompts or slash commands"},
	{Name: "/oneoff", Description: "Run a task in an isolated work-capable fork"},
	{Name: "/btw", Description: "Ask a side question without changing Main"},
	{Name: "/ps", Description: "Show running processes"},
	{Name: "/predictions", Description: "Enable or disable next-message suggestions: on|off"},
	{Name: "/session", Description: "Show request metrics, cache and transport"},
	{Name: "/title", HideRemote: true, Description: "Rename this session"},
	{Name: "/status", Description: "Show session settings and usage limits"},
	{Name: "/copy", HideRemote: true, Description: "Copy the last response or part of it"},
	{Name: "/rewind", Description: "Rewind before a user turn; keep or revert captured edits"},
	{Name: "/fork", Description: "Fork this conversation and switch to the branch"},
	{Name: "/resume", Description: "Resume a saved session"},
	{Name: "/archive", Description: "Archive this session"},
	{Name: "/compact", Description: "Compact context now; Tab queues while busy"},
	{Name: "/clear", Description: "Clear the transcript and start a new session"},
	{Name: "/lock", HideRemote: true, Description: "Prevent accidental keyboard interruption"},
	{Name: "/unlock", HideRemote: true, Description: "Allow keyboard interruption again"},
	{Name: "/quit", Description: "Quit the session"},
	{Name: "/detach", HideRemote: true, Description: "Leave the terminal; keep the session running"},
}

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:268:310 and
// codex-rs/tui/src/app/event_dispatch.rs:385:401@68e1a421.
// Clear starts a fresh host thread.
// Compact uses its native RPC after acknowledged interruption, never a
// turn/steer text payload. Tab can explicitly queue it instead.
func (session *nativeSession) sessionCommand(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, parent context.Context, provider string, command string) error {
	if session.Client.Thread == "" || session.restoring != nil || session.Client.Replacement.Pending() {
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the session to be ready", false)
		return nil
	}
	if command == "/compact" {
		return session.submitCompact(attached, picker, editor, clock, parent, false)
	}
	if sessionBusy(&session.Client.Live, attached.conversation) {
		setNotice(&attached.feedback, presentationTime(clock), command+" is disabled while a task is in progress", true)
		return nil
	}
	if command == "/rewind" {
		if !session.Client.HasUserTurn {
			setNotice(&attached.feedback, presentationTime(clock), "/rewind is disabled before the first user turn", false)
			return nil
		}
		showRewindPicker(&session.Client, picker, session.review)
		(*editor).Take()
		return nil
	}
	prepareDraft(picker, attached.cwd, session.skillEnvironment, (*editor).Take())
	if command == "/fork" {
		if err := session.Client.Requests.Fork(&session.Client.Startup, &session.Client.Live, session.settings, provider, session.approvalMode); err != nil {
			setNotice(&attached.feedback, presentationTime(clock), "Could not fork session: "+err.Error(), true)
			return nil
		}
		attached.presentation.Status.SetText("Forking session…")
		return nil
	}
	session.Client.Replacement.Clear()
	attached.presentation.Status.SetText("Starting new session…")
	params := sessioncontrol.ThreadPermissions(map[string]any{"sessionStartSource": "clear"}, session.approvalMode)
	if attached.cwd != "" {
		params["cwd"] = attached.cwd
	}
	if session.settings.Model != "" {
		params["model"] = session.settings.Model
	}
	if provider != "" {
		params["modelProvider"] = provider
	}
	if session.settings.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": session.settings.Effort}
	}
	if session.settings.Tier != "" {
		params["serviceTier"] = session.settings.Tier
	}
	if err := session.Client.Request("thread/start", params); err != nil {
		session.Client.Replacement.Finish()
		return err
	}
	return nil
}

func (session *nativeSession) submitCompact(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, parent context.Context, queue bool) error {
	if session.Client.Thread == "" || session.restoring != nil || session.Client.Replacement.Pending() {
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the session to be ready", false)
		return nil
	}
	draft := prepareDraft(picker, attached.cwd, session.skillEnvironment, (*editor).Take())
	draft.Text = "/compact"
	draft.Compact = &conversationmodel.CompactIntent{WaitForIdle: queue, Continue: session.Client.Turn != "" || session.Client.Starting()}
	attached.conversation.Unsent = append(attached.conversation.Unsent, draft)
	return session.flushInput(attached, picker, clock, editor, parent)
}

// sessionBusy reports work that leaving the current thread would strand.
func sessionBusy(control *sessioncontrol.Live, conversation *conversationmodel.Session) bool {
	return control.Busy(len(conversation.Unsent)+len(conversation.Queued) > 0)
}

// A queued compact is a local action, not Main's running turn. Return waiting
// input for retry without interrupting that turn or an in-flight submission.
func cancelQueuedCompact(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time) bool {
	isCompact := func(d conversationmodel.Input) bool { return d.Compact != nil }
	hasQueued := slices.ContainsFunc(attached.conversation.Unsent, isCompact) || slices.ContainsFunc(attached.conversation.Queued, isCompact)
	if !control.Compaction.CancelQueued(hasQueued) {
		return false
	}
	restoreDrafts(attached, questions, approvals, picker, control.Turn, clock, slices.Concat(attached.conversation.Unsent, attached.conversation.Queued)...)
	attached.conversation.Unsent, attached.conversation.Queued = nil, nil
	setNotice(&attached.feedback, presentationTime(clock), "Queued compaction cancelled · input restored · Main continues", false)
	return true
}

// resetDiffScope starts the saved Diff over for a resumed root thread; its
// restoration then includes only that thread and its descendants.
func resetDiffScope(attached *attachedPresentation, review *sessionreview.Review, source *changecapture.Service, parent context.Context) {
	if review == nil {
		return
	}
	review.Replace(parent)
	publishReview(attached, review, source)
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
