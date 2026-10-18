package launch

import (
	"io"
	"iter"
	"os"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// replayAttachment binds only recorded source owners and their terminal consumer.
// Replacement retires this lifetime; it carries no live controller or authority.
type replayAttachment struct {
	detailFiles
	session             sessionpresentation.Attachment
	presentation        *sessionpresentation.Model
	conversation        *conversationmodel.Session
	source, childSource *sessionpresentation.ActivitySource
	view, agents        *activityui.View
	shell               *terminalsession.Attachment
	streams             terminalsession.Streams
	transcript          observation.Transcript
	waits               activitypresentation.Waits
	feedback            noticepresentation.Model
	cwd, Turn           string
}

func (p *uiReplayPlayback) reset() {
	previous := p.ui
	if previous != nil {
		p.close()
	}
	p.paintedAt = time.Time{}
	u := &replayAttachment{cwd: p.Source.Cwd}
	u.session.Start(p.Source.Thread)
	u.presentation = p.views.Session(u.session.Scope())
	u.presentation.Attach(sessionpresentation.Thread{ID: p.Source.Thread, Caller: "/root"}, true)
	u.presentation.Status.Set("Replay", false)
	u.conversation = p.conversation.Session(u.session.Scope())
	u.transcript = p.observations.NewTranscript(u.session.Scope())
	state := u.session.Activity()
	state.StartRoster(sessionpresentation.ActivityAgent{Thread: p.Source.Thread, Name: "/root", Role: "main"})
	u.source = sessionpresentation.NewActivitySource(sessionpresentation.MainAudience)
	u.childSource = sessionpresentation.NewActivitySource(sessionpresentation.ChildAudience)
	u.source.Model, u.childSource.Model = &state.Main, &state.Children
	u.childSource.Coverage = ""
	u.shell, _ = p.terminalViews.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	u.view, u.agents = u.shell.Transcript, u.shell.Agents
	u.view.Clock, u.agents.Clock = func() time.Time { return p.At }, func() time.Time { return p.At }
	if previous != nil {
		u.fileReadSlot = previous.fileReadSlot
		u.shell.Focus, u.shell.DiffOpen = previous.shell.Focus, previous.shell.DiffOpen
	}
	u.shell.Shell.Clamp()
	p.ui = u
	p.publish()
}

func (p *uiReplayPlayback) publish() {
	u := p.ui
	processes := 0
	for range u.session.RunningProcesses() {
		processes++
	}
	view := u.presentation.Publish(sessionpresentation.MainInputs{
		Info:         sessionpresentation.MainInfo{Workspace: u.cwd, Skills: u.source.ActiveSkills()["Main"]},
		RecordedTurn: u.Turn, Conversation: u.conversation, Feedback: &u.feedback, Processes: processes,
	}, sessionCaption(&u.session, u.presentation, sessionpresentation.Caption{}, nil), p.At)
	u.shell.Main.Update(view)
	u.shell.Prompts.Update(view)
	u.view.Painter.FileLink, u.agents.Painter.FileLink = markdownFileLinks(u.cwd), markdownFileLinks(u.cwd)
	u.publishActivity()
}

func (u *replayAttachment) publishActivity() {
	refreshRosterRoles(&u.session, u.childSource)
	u.view.StatusLabel = textsanitize.Safe(terminalsession.StatusText(u.presentation.View().Status.State), false)
	deliverActivity(u.view, u.source, u.presentation, nil, nil)
	deliverActivity(u.agents, u.childSource, u.presentation, u.source, u.view)
}

func (u *replayAttachment) activitySource(view *activityui.View) *sessionpresentation.ActivitySource {
	if view == u.view {
		return u.source
	}
	if view == u.agents {
		return u.childSource
	}
	return nil
}

func (u *replayAttachment) transcriptView() *activityui.View {
	if u.presentation.DisplayingChild() {
		u.agents.Conversation = true
		return u.agents
	}
	return u.view
}

// Recorded messages use Conversation's admission and source projection, without
// binding response handles or opening an editable prompt.
func (p *uiReplayPlayback) transcript(e observation.Event) bool {
	if e.Kind != observation.ReasoningDelta && e.Kind != observation.ReasoningPart && e.Kind != observation.AgentDelta && e.Item.Type != "reasoning" && e.Item.Type != "agentMessage" && e.Item.Type != "userMessage" {
		return false
	}
	u := p.ui
	text := binding.RetainedTranscript(e)
	var userText string
	if e.Item.Type == "userMessage" {
		userText, _, _ = sessionpresentation.UserText(e.Item.Content)
	}
	changes := u.conversation.Questions.ObserveMessage(conversationbinding.MessageQuestions(text, e.Item, userText, e.ThreadID == p.Source.Thread, false))
	if changes.Added != nil {
		sessionpresentation.RecordQuestion(changes.Added, "", u.Turn, p.At, u.source, u.childSource)
	}
	for _, change := range changes.Changed {
		sessionpresentation.RecordQuestion(change.Call, "", u.Turn, p.At, u.source, u.childSource)
	}
	if changes.Suppress {
		return true
	}
	deliveries := sessionpresentation.ProjectMessage(&u.session, u.source, u.childSource, u.transcript, e.ThreadID == p.Source.Thread, sessionpresentation.ObservationInput{Event: e, Text: text, Main: p.Source.Thread, Workspace: u.cwd})
	u.streams.Deliver(deliveries, u.view, u.agents)
	u.presentation.ObserveProgress(e, u.Turn, false)
	return true
}

func (u *replayAttachment) flushStreamOutput(at time.Time) {
	u.streams.AdvanceReasoning(at, u.view, u.agents)
	u.streams.AdvanceCommands(at, nil, u.view, u.agents)
}

func (p *uiReplayPlayback) frameInput() terminalsession.FrameInput {
	in := p.ui.shell.FrameInput(p.At, mekugiVersion())
	in.Playback, in.PlaybackBar = true, p.bar(p.ui.shell.Width)
	return in
}

// Playback consumes only observational terminal effects. Recorded content and
// future terminal effects cannot reach live composer or request dispatch.
func (p *uiReplayPlayback) applyInput(effects iter.Seq[terminalsession.InputEffect]) error {
	p.publish()
	defer p.publish()
	for effect := range effects {
		switch {
		case effect.Display != nil:
			if err := p.ui.presentation.Display(*effect.Display); err != nil {
				p.ui.feedback.Set(err.Error(), true, p.At)
			}
		case effect.Roster != nil:
			intent := effect.Roster
			target := p.ui.presentation.ActivityTarget(intent.Caller, p.ui.childSource)
			if err := p.ui.presentation.Display(sessionpresentation.DisplayIntent{Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision}, Thread: target}); err == nil {
				p.ui.agents.Selected = target.Caller
			} else {
				p.ui.feedback.Set(err.Error(), true, p.At)
			}
		case effect.Dialog != nil:
			p.outputIntent(*effect.Dialog)
		default:
			return nil
		}
		p.publish()
	}
	return nil
}

func (p *uiReplayPlayback) outputIntent(intent terminalsession.DialogIntent) {
	u := p.ui
	switch intent.Action {
	case terminalsession.DialogClose:
		u.detailFiles.closeOutput(&u.session, u.shell)
	case terminalsession.DialogCopy:
		text, alert := u.shell.Frame.CopyText(intent.Text)
		u.feedback.Set(text, alert, p.At)
	case terminalsession.DialogReference:
		if err := u.presentation.Reference(u.shell.Main.Reference("dialog", intent.Text, ""), u.conversation); err != nil {
			u.feedback.Set(err.Error(), true, p.At)
		}
	case terminalsession.DialogOpenLink:
		u.detailFiles.openMarkdownFile(&u.session, u.presentation, u.cwd, u.activitySource(u.shell.Dialog.View), u.shell, u.shell.Dialog.View, intent.Text)
	}
	p.publish()
}

func (p *uiReplayPlayback) paintFrame(out io.Writer, width, height int) error {
	u := p.ui
	u.shell.Width, u.shell.Height = max(1, width), max(1, height)
	u.shell.Main.ContentPainted = false
	u.detailFiles.prepareOutput(&u.session, u.shell)
	view := u.transcriptView()
	return paintNative(u.shell, p.frameInput(), view, u.activitySource(view), p.ctx, out)
}
