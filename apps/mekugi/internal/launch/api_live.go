// Source: apps/mekugi/internal/launch/web_live.go:1:148@[14a6d1f3]
package launch

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// apiLive publishes the owned runtime independently of attached viewers.
type apiLive struct {
	server       *sessionapi.Server
	main, agents commandFeed
	attachment   uint64
	started      time.Time
	projected    time.Time
	failed       bool
}

// apiFrameInterval bounds detached projection work, including with no viewers.
const apiFrameInterval = 100 * time.Millisecond

// publish is called only by the session owner loop.
func (w *apiLive) publish(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], restoring, childSummary bool, now time.Time) {
	view := attached.presentation.View()
	waiting := apiWaiting(attached, control)
	err := w.server.SetSummary(sessionapi.Summary{Thread: control.Thread, Title: view.Info.Title, Workspace: attached.cwd, Started: w.started, Status: view.Status.Session, Waiting: waiting, Ready: apiInputRefusal(attached, control, restoring) == ""})
	if err == nil && now.Sub(w.projected) >= apiFrameInterval {
		w.projected = now
		if view.Version.Attachment != w.attachment {
			w.main, w.agents, w.attachment = commandFeed{}, commandFeed{}, view.Version.Attachment
		}
		err = w.server.Publish(sessionapi.Frame{
			Session:    sessionapi.Session{Thread: control.Thread, Title: view.Info.Title, Workspace: attached.cwd, Started: w.started.UnixMilli()},
			Input:      &sessionapi.Input{Reason: apiInputRefusal(attached, control, restoring), Stoppable: sessionpresentation.MainInterruptible(&control.Live, attached.conversation), Waiting: waiting},
			Prompts:    apiPrompts(attached, control),
			Attachment: view.Version.Attachment,
			Status:     view.Status.Session,
			Main:       w.main.publication(attached.source, attached.source.Publication(attached.presentation, nil)),
			Agents:     w.agents.publication(attached.childSource, attached.childSource.Publication(attached.presentation, attached.source)),

			ChildReplySummary: childSummary,
		})
	}
	if err != nil && !w.failed {
		w.failed = true
		setNotice(&attached.feedback, terminal, now, "Session publication unavailable: "+err.Error(), true)
	}
}

// apiWaiting counts the questions and approvals Main waits on.
func apiWaiting(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest]) int {
	return attached.conversation.Questions.View(control.Thread).Count + attached.conversation.Approvals.View().Count
}

// apiInputRefusal shares Main's ordinary admission rules.
func apiInputRefusal(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], restoring bool) string {
	switch {
	case control.Thread == "" || restoring || control.Replacement.Pending():
		return "Wait for the session to be ready."
	case !attached.conversation.DraftView().Available:
		return "Answer the open prompt first."
	}
	return ""
}

// applyAPIIntent applies an admitted request through the existing session owners.
// An error ends the runtime, matching the local control path.
func applyAPIIntent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, intent sessionapi.Intent) (string, error) {
	if reason := apiIntentRefusal(attached, control, intent); reason != "" {
		return reason, nil
	}
	switch intent.Action {
	case sessionapi.AnswerQuestions:
		answers, incomplete, refusal := prepareAPIAnswers(attached, control, intent.Answer)
		if refusal != "" {
			return refusal, nil
		}
		for _, answer := range answers {
			if !answerQuestion(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, *side, clock, questions, approvals, overlay, sessionpresentation.AnswerIntent{Version: attached.presentation.View().Version, AnswerInput: answer}) {
				return "This prompt was already answered.", nil
			}
		}
		return "", submitQuestions(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, *side, clock, questions, approvals, overlay, environment, *owned, hook, parent, observations, reviewOwner, receipts, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: intent.Answer.Call, Incomplete: incomplete})
	case sessionapi.DecideApproval:
		_, choice, ok := attached.conversation.Approvals.Decision(intent.Decide.Approval, intent.Decide.Choice)
		if !ok {
			return "This approval or choice is no longer available.", nil
		}
		if intent.Decide.Reason != "" && !choice.Deny {
			return "A reason can only accompany a denial.", nil
		}
		return "", decideApproval(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, *side, clock, questions, approvals, overlay, notifications, reviewOwner, receipts, observations, sessionpresentation.ApprovalIntent{Version: attached.presentation.View().Version, Request: intent.Decide.Approval, Choice: intent.Decide.Choice, Reason: intent.Decide.Reason})
	case sessionapi.Send:
		// Enter semantics: steer the active turn, or start one while idle.
		if reason := apiInputRefusal(attached, control, restoring); reason != "" {
			return reason, nil
		}
		if text := strings.TrimSpace(intent.Text); strings.HasPrefix(text, "/") {
			return "Slash commands are not available through this input request.", nil
		} else if strings.HasPrefix(text, "!") {
			return "Send shell commands through the shell-input request.", nil
		}
		// Unlike Enter in the terminal, this leaves the terminal's notice and
		// scroll position alone.
		defer settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
		plan := control.PlanInput(sessioncontrol.SendInput, attached.conversation.Questions.Waiting(control.Thread))
		var input conversationmodel.Input
		if intent.Prepared != nil {
			if *owned == nil {
				*owned = make(map[string]bool)
			}
			if !intent.Prepared.Admit(intent.Context, *owned) {
				return "The prepared input was canceled before admission.", nil
			}
			input = conversationmodel.Input{Draft: intent.Prepared.Draft}
			if len(input.Attachments) == 0 && len(input.Images) == 0 {
				input = prepareDraft(picker, terminal, attached.cwd, environment, input.Draft)
			}
			if input.AttachmentNotice != "" {
				setNotice(&attached.feedback, terminal, presentationTime(clock), input.AttachmentNotice, true)
			}
		} else {
			input = prepareDraft(picker, terminal, attached.cwd, environment, composer.Draft{Text: intent.Text})
		}
		attached.conversation.QueueInput(input, plan.Queued)
		if !plan.Flush {
			return "", nil
		}
		return "", flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
	case sessionapi.Stop:
		// Ctrl-C semantics for Main, even while the terminal displays a side
		// conversation; the terminal's lock guards only its keyboard.
		view := publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), presentationTime(clock))
		if !sessionpresentation.MainInterruptible(&control.Live, attached.conversation) {
			return "Nothing is running.", nil
		}
		return "", interruptSession(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, settings, tiers, title, backend, restoring, side, overlay, reviewOwner, receipts, sessionpresentation.InterruptIntent{Version: view.Version, Target: control.InterruptTarget(), Mode: sessioncontrol.RestoreWaiting})
	}
	return "This session does not offer that request.", nil
}

// apiIntentRefusal rechecks captured request identity on the owner goroutine.
// Request cancellation before admission never transfers execution ownership.
func apiIntentRefusal(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], intent sessionapi.Intent) string {
	if intent.Context != nil && intent.Context.Err() != nil {
		return "The request was canceled before admission."
	}
	if intent.Thread != "" && intent.Thread != control.Thread || intent.Workspace != "" && intent.Workspace != attached.cwd {
		return "The requested session destination is unavailable."
	}
	return ""
}

// Source: apps/mekugi/internal/launch/web.go:41:82@[14a6d1f3] commandFeed
// commandFeed names compound result rows by the command that ran, which the
// browser shows where the terminal labels a shell batch; publications carry
// command input only on request. The detached copy and each entry's command
// are kept while their sources are unchanged, so an unchanged frame stays
// identical and costs nothing to publish.
type commandFeed struct {
	base, view *activitypresentation.Publication
	content    map[uint64]commandContent
}

type commandContent struct {
	revision uint64
	content  *activitypresentation.ItemContent
}

func (c *commandFeed) publication(source *sessionpresentation.ActivitySource, base *activitypresentation.Publication) *activitypresentation.Publication {
	if base == c.base {
		return c.view
	}
	c.base, c.view = base, base
	if c.content == nil {
		c.content = map[uint64]commandContent{}
	}
	for i, entry := range base.Entries {
		// Matches the rows sessionapi's projection names by their command.
		if !slices.ContainsFunc(entry.Values, func(v activitypresentation.Value) bool { return v.BatchExit && v.Code == "" }) {
			continue
		}
		if c.view == base {
			c.view = new(*base)
			c.view.Entries = slices.Clone(base.Entries)
		}
		cached, ok := c.content[entry.Seq]
		if !ok || cached.revision != entry.Revision {
			cached = commandContent{entry.Revision, new(sessionpresentation.ActivityContent(source.Entries[i].Source))}
			c.content[entry.Seq] = cached
		}
		c.view.Entries[i].Command = cached.content
	}
	return c.view
}
