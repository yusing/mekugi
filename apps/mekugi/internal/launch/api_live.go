// Source: apps/mekugi/internal/launch/web_live.go:1:148@[14a6d1f3]
package launch

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// apiLive publishes the owned runtime independently of attached viewers.
type apiLive struct {
	server       *sessionapi.Server
	coordination *control.Service
	main, agents commandFeed
	attachment   uint64
	started      time.Time
	projected    time.Time
	failed       bool
}

// apiFrameInterval bounds detached projection work, including with no viewers.
const apiFrameInterval = 100 * time.Millisecond

// publish is called only by the session owner loop.
func (w *apiLive) publish(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], review *sessionreview.Review, reviewSource *changecapture.Service, restoring, childSummary bool, now time.Time) {
	view := attached.presentation.View()
	waiting := apiWaiting(attached, control)
	err := w.server.SetSummary(sessionapi.Summary{Thread: control.Thread, Title: view.Info.Title, Workspace: attached.cwd, Started: w.started, Status: view.Status.Session, Waiting: waiting, Ready: apiInputRefusal(attached, control, restoring) == ""})
	if err == nil && now.Sub(w.projected) >= apiFrameInterval {
		w.projected = now
		if view.Version.Attachment != w.attachment {
			w.main, w.agents, w.attachment = commandFeed{}, commandFeed{}, view.Version.Attachment
		}
		caller, role := "/root", ""
		if attached.threadInput != nil {
			if attached.threadInput.caller != "" {
				caller = attached.threadInput.caller
			}
			role = attached.threadInput.role
		}
		frame := sessionapi.Frame{
			Session:    sessionapi.Session{Thread: control.Thread, Title: view.Info.Title, Workspace: attached.cwd, Started: w.started.UnixMilli(), Caller: caller, Role: role},
			Input:      &sessionapi.Input{Reason: apiInputRefusal(attached, control, restoring), Stoppable: sessionpresentation.MainInterruptible(&control.Live, attached.conversation), Waiting: waiting},
			Prompts:    apiPrompts(attached, control),
			Questions:  nativePendingQuestions(attached.conversation),
			Attachment: view.Version.Attachment,
			Status:     view.Status.Session,
			Main:       w.main.publication(attached.source, attached.source.Publication(attached.presentation, nil)),
			Agents:     w.agents.publication(attached.childSource, attached.childSource.Publication(attached.presentation, attached.source)),
			Review:     new(reviewSource.ReviewView(attached.session.Scope())),

			ChildReplySummary: childSummary,
		}
		frame.FamilyRoot, frame.Members = w.coordination.Family(control.Thread)
		if review != nil {
			frame.LiveScope = review.Scope()
			if previews := review.Preview(); previews != nil {
				frame.Live = previews.Current()
			}
		}
		err = w.server.Publish(frame)
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
func (session *nativeSession) applyAPIIntent(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, overlay bool, parent context.Context, intent sessionapi.Intent) (string, error) {
	if reason := apiIntentRefusal(attached, &session.Client, intent); reason != "" {
		return reason, nil
	}
	switch intent.Action {
	case sessionapi.AnswerQuestions:
		answers, incomplete, refusal := prepareAPIAnswers(attached, &session.Client, intent.Answer)
		if refusal != "" {
			return refusal, nil
		}
		if intent.Prepared != nil && !intent.Prepared.Admit(intent.Context, session.ownedImages) {
			return "The answer was canceled before admission.", nil
		}
		for _, answer := range answers {
			if !session.answerQuestion(attached, terminal, picker, editor, *side, clock, overlay, sessionpresentation.AnswerIntent{Version: attached.presentation.View().Version, AnswerInput: answer}) {
				return "This prompt was already answered.", nil
			}
		}
		return "", session.submitQuestions(attached, terminal, picker, editor, *side, clock, overlay, parent, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: intent.Answer.Call, Incomplete: incomplete})
	case sessionapi.DecideApproval:
		decision := sessionpresentation.ApprovalIntent{Version: attached.presentation.View().Version, Request: intent.Decide.Approval, Choice: intent.Decide.Choice, Reason: intent.Decide.Reason}
		_, choice, err := attached.presentation.Approval(decision, &attached.conversation.Approvals)
		if err != nil {
			return err.Error(), nil
		}
		if choice.Session {
			return "Reusable approval grants require an attached control view. Use view/intent.", nil
		}
		return "", session.decideApproval(attached, terminal, picker, editor, *side, clock, overlay, decision)
	case sessionapi.Send:
		// Enter semantics: steer the active turn, or start one while idle.
		if reason := apiInputRefusal(attached, &session.Client, session.restoring != nil); reason != "" {
			return reason, nil
		}
		if text := strings.TrimSpace(intent.Text); strings.HasPrefix(text, "/") {
			return "Slash commands are not available through this input request.", nil
		} else if strings.HasPrefix(text, "!") {
			return "Send shell commands through the shell-input request.", nil
		}
		// Unlike Enter in the terminal, this leaves the terminal's notice and
		// scroll position alone.
		defer settlePresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, overlay)
		plan := session.Client.PlanInput(sessioncontrol.SendInput, attached.conversation.Questions.Waiting(session.Client.Thread))
		var input conversationmodel.Input
		if intent.Prepared != nil {
			if session.ownedImages == nil {
				session.ownedImages = make(map[string]bool)
			}
			if !intent.Prepared.Admit(intent.Context, session.ownedImages) {
				return "The prepared input was canceled before admission.", nil
			}
			input = conversationmodel.Input{Draft: intent.Prepared.Draft}
			if len(input.Attachments) == 0 && len(input.Images) == 0 {
				input = prepareDraft(picker, terminal, attached.cwd, session.skillEnvironment, input.Draft)
			}
			if input.AttachmentNotice != "" {
				setNotice(&attached.feedback, terminal, presentationTime(clock), input.AttachmentNotice, true)
			}
		} else {
			input = prepareDraft(picker, terminal, attached.cwd, session.skillEnvironment, composer.Draft{Text: intent.Text})
		}
		attached.conversation.QueueInput(input, plan.Queued)
		if !plan.Flush {
			return "", nil
		}
		return "", session.flushInput(attached, terminal, picker, clock, editor, parent)
	case sessionapi.Stop:
		// Ctrl-C semantics for Main, even while the terminal displays a side
		// conversation; the terminal's lock guards only its keyboard.
		view := publishPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
		if !sessionpresentation.MainInterruptible(&session.Client.Live, attached.conversation) {
			return "Nothing is running.", nil
		}
		return "", session.interruptSession(attached, terminal, picker, editor, clock, side, overlay, sessionpresentation.InterruptIntent{Version: view.Version, Target: session.Client.InterruptTarget(), Mode: sessioncontrol.RestoreWaiting})
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
	view := attached.presentation.View()
	prepared := control.Startup.Prepared != nil && (attached.threadInput == nil || !attached.threadInput.admitted)
	if (intent.Action == sessionapi.Send || intent.Action == sessionapi.RunShell) && (prepared || view.Admission.ReadOnly || view.ReadOnly != sessionpresentation.Editable) {
		return sessionpresentation.ErrInputUnavailable.Error()
	}
	return ""
}

// Source: apps/mekugi/internal/launch/web.go:41:82@[14a6d1f3] commandFeed
// commandFeed supplies original command input for compound result rows that
// lack source. Publications carry command input only on request. The detached
// copy and each entry's command
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
