package launch

import (
	"io"
	"iter"
	"os"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

// replayOwners are the shared presentation owners from which each recorded scope
// is constructed. They carry no renderer, host controller or authority.
type replayOwners struct {
	views        *sessionpresentation.Service
	conversation *conversationmodel.Service
	observations *binding.Observation
	activity     *activitymodel.Service
}

// sessionView publishes the scope's detached session values at the recorded time.
func (s *replayAttachment) sessionView(at time.Time) sessionpresentation.View {
	processes := 0
	for range s.session.RunningProcesses() {
		processes++
	}
	return s.presentation.Publish(sessionpresentation.MainInputs{
		Info:         sessionpresentation.MainInfo{Workspace: s.cwd, Skills: s.source.ActiveSkills()["Main"]},
		RecordedTurn: s.Turn, Conversation: s.conversation, Feedback: &s.feedback, Processes: processes,
	}, sessionCaption(&s.session, s.presentation, sessionpresentation.Caption{}, nil), at)
}

// apply admits one recorded observation. Playback selects recorded turns
// without creating or settling live control operations.
func (s *replayAttachment) apply(o replayOwners, source *playback.Source, at time.Time, e playback.Event) {
	if id := e.ThreadID; id != "" && id != source.Thread && s.session.KnownCaller(id) == "" {
		s.session.RegisterThread(observation.Thread{ID: id, Nickname: strings.TrimPrefix(source.Threads[id], "/root/")})
	}
	switch {
	case e.Hook != nil:
		entry := hookEntry(&s.session, o.activity, s.cwd, e.ThreadID, *e.Hook, false)
		entry.Seq = s.session.Activity().NextSequence()
		sessionpresentation.ApplyActivity(s.source, s.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
	case e.Kind == observation.ProviderStarted:
		if e.ThreadID == "" {
			return
		}
		updates := s.transcript.ProviderStarted(e.ThreadID, e.At, s.session.Thread(e.ThreadID))
		sessionpresentation.ApplyActivity(s.source, s.childSource, s.session.AdmitText(updates, source.Thread), nil)
	case e.Kind == observation.ProviderCompleted:
	default:
		if s.message(source, at, e.Event) {
			return
		}
		result := sessionpresentation.ReduceObservation(&s.session, s.presentation, s.source, s.childSource, s.transcript, o.activity.Outputs(s.session.Scope()), sessionpresentation.ObservationInput{Event: e.Event, Text: binding.RetainedTranscript(e.Event), Main: source.Thread, Selected: s.Turn, Workspace: s.cwd})
		if result.Publish && s.shell != nil {
			s.streams.Deliver(result.Deliveries, s.view, s.agents)
			s.publishActivity()
		}
		// Status uses the same admitted observation clock.
		if e.ThreadID == source.Thread {
			switch e.Kind {
			case observation.TurnStarted:
				if result.Started {
					s.Turn = e.TurnID
					s.presentation.Status.Started = e.At
					s.presentation.Status.Set("Working", false)
				}
			case observation.TurnCompleted:
				if e.TurnID == s.Turn {
					s.Turn = ""
					s.presentation.Status.Complete(e.TurnStatus, nil, e.At)
					for _, call := range s.conversation.Questions.Calls {
						if !call.Resolved {
							sessionpresentation.RecordQuestion(call, "", s.Turn, at, s.source, s.childSource)
						}
					}
				}
			}
		}
	}
}

// Recorded messages use Conversation's admission and source projection, without
// binding response handles or opening an editable prompt.
func (s *replayAttachment) message(source *playback.Source, at time.Time, e observation.Event) bool {
	if e.Kind != observation.ReasoningDelta && e.Kind != observation.ReasoningPart && e.Kind != observation.AgentDelta && e.Item.Type != "reasoning" && e.Item.Type != "agentMessage" && e.Item.Type != "userMessage" {
		return false
	}
	text := binding.RetainedTranscript(e)
	var userText string
	if e.Item.Type == "userMessage" {
		userText, _, _ = sessionpresentation.UserText(e.Item.Content)
	}
	changes := s.conversation.Questions.ObserveMessage(conversationbinding.MessageQuestions(text, e.Item, userText, e.ThreadID == source.Thread, false))
	if changes.Added != nil {
		sessionpresentation.RecordQuestion(changes.Added, "", s.Turn, at, s.source, s.childSource)
	}
	for _, change := range changes.Changed {
		sessionpresentation.RecordQuestion(change.Call, "", s.Turn, at, s.source, s.childSource)
	}
	if changes.Suppress {
		return true
	}
	deliveries := sessionpresentation.ProjectMessage(&s.session, s.source, s.childSource, s.transcript, e.ThreadID == source.Thread, sessionpresentation.ObservationInput{Event: e, Text: text, Main: source.Thread, Workspace: s.cwd})
	if s.shell != nil {
		s.streams.Deliver(deliveries, s.view, s.agents)
	}
	s.presentation.ObserveProgress(e, s.Turn, false)
	return true
}

// replayAttachment owns the recorded presentation and its terminal consumer.
type replayAttachment struct {
	session             sessionpresentation.Attachment
	presentation        *sessionpresentation.Model
	conversation        *conversationmodel.Session
	source, childSource *sessionpresentation.ActivitySource
	transcript          observation.Transcript
	feedback            noticepresentation.Model
	cwd, Turn           string
	detailFiles
	view, agents *activityui.View
	shell        *terminalsession.Attachment
	streams      terminalsession.Streams
}

func (o replayOwners) attachment(source *playback.Source) *replayAttachment {
	u := &replayAttachment{cwd: source.Cwd}
	u.session.Start(source.Thread)
	u.presentation = o.views.Session(u.session.Scope())
	u.presentation.Attach(sessionpresentation.Thread{ID: source.Thread, Caller: "/root"}, true)
	u.presentation.Status.Set("Replay", false)
	u.conversation = o.conversation.Session(u.session.Scope())
	u.transcript = o.observations.NewTranscript(u.session.Scope())
	state := u.session.Activity()
	state.StartRoster(sessionpresentation.ActivityAgent{Thread: source.Thread, Name: "/root", Role: "main"})
	u.source = &sessionpresentation.ActivitySource{Model: &state.Main, Audience: sessionpresentation.MainAudience, Coverage: "CONNECTING"}
	u.childSource = &sessionpresentation.ActivitySource{Model: &state.Children, Audience: sessionpresentation.ChildAudience}
	return u
}

func (p *uiReplayPlayback) reset() {
	previous := p.ui
	if previous != nil {
		p.close()
	}
	p.paintedAt = time.Time{}
	u := p.replayOwners.attachment(p.Source)
	u.shell, _ = p.terminalViews.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	u.view, u.agents = u.shell.Transcript, u.shell.Agents
	u.view.Clock, u.agents.Clock = func() time.Time { return p.At }, func() time.Time { return p.At }
	if previous != nil {
		u.fileReadSlot = previous.fileReadSlot
		u.links = previous.links
		u.shell.Focus, u.shell.DiffOpen = previous.shell.Focus, previous.shell.DiffOpen
	}
	u.shell.Shell.Clamp()
	p.ui = u
	p.publish()
}

func (p *uiReplayPlayback) publish() {
	u := p.ui
	view := u.sessionView(p.At)
	u.shell.Main.Update(view)
	u.shell.Prompts.Update(view)
	u.view.Painter.FileLink, u.agents.Painter.FileLink = markdownFileLinks(u.cwd), markdownFileLinks(u.cwd)
	u.view.Painter.DeferFileLinks, u.agents.Painter.DeferFileLinks = true, true
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
		u.detailFiles.closeOutput(u.shell)
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
	return paintNative(&u.detailFiles, u.shell, p.frameInput(), view, u.activitySource(view), p.ctx, out)
}
