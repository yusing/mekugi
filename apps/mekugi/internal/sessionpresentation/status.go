package sessionpresentation

import (
	"reflect"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// MainState retains presentation facts, never operation authority. Completion
// timing remains a duration until a consumer chooses its display grammar.
type MainState struct {
	Text            string
	Alert           bool
	Started         time.Time
	completed       time.Duration
	completionKnown bool
	failure         string
	failureKnown    bool
}

func (s *MainState) Set(text string, alert bool) {
	s.Text, s.Alert = text, alert
	s.completed, s.completionKnown, s.failure, s.failureKnown = 0, false, "", false
}

func (s *MainState) SetText(text string) { s.Set(text, s.Alert) }

func (s *MainState) Complete(status string, failure *string, now time.Time) {
	s.Set(strings.ToUpper(status[:min(1, len(status))])+status[min(1, len(status)):], status == "failed" || failure != nil)
	if status == "completed" && !s.Started.IsZero() {
		s.completed, s.completionKnown = now.Sub(s.Started), true
	}
	if failure != nil {
		s.failure, s.failureKnown = *failure, true
	}
}

type Animation uint8

const (
	Static Animation = iota
	Pulse
	Working
)

// Label keeps full text and timing. Sanitization, previews and animation frames
// belong to the consumer, not the semantic status selection.
type Label struct {
	Text, Failure                 string
	Alert, Running                bool
	Animation                     Animation
	Elapsed, Completion           time.Duration
	ElapsedKnown, CompletionKnown bool
	FailureKnown                  bool
}

type MainStatus struct {
	State, Session, Composer, Feedback Label
	ComposerOverride                   bool
	Details                            [2]Label
	ShellMode                          bool
	Processes                          int
	Animating                          bool
}

// MainInputs borrows current owner state only for publication. Nothing here is
// retained, and the resulting view contains no owner pointers or capabilities.
type MainInputs struct {
	Resume                 *ResumeView
	Control                *sessioncontrol.Live
	Feedback               *noticepresentation.Model
	Conversation           *conversationmodel.Session
	Restoring, SideRunning bool
	Processes              int
	History                []HistoryView
	Panel                  *PanelView
}

type Caption struct {
	Model, Effort, Tier          string
	ContextTokens, ContextWindow uint64
	ContextKnown                 bool
	Items                        []presentation.Item
}

// Publish joins current owner facts with this attachment's retained progress.
// The caption already names the displayed thread, including unknown settings.
// Source: apps/mekugi/internal/router/app_server_ui.go:1550:1585 and
// composer_notice.go:32:74@1145fbbc.
func (m *Model) Publish(in MainInputs, caption Caption, now time.Time) (v View) {
	v = m.View()
	v.Status = MainStatus{}
	defer func() {
		if !reflect.DeepEqual(v, m.view) {
			v.Version.Revision++
			m.view = cloneView(v)
		}
		v = m.View()
	}()
	v.Caption = caption
	v.Panel = in.Panel
	v.Resume = in.Resume
	v.Questions = in.Conversation.Questions.View(in.Control.Thread)
	v.Approvals = in.Conversation.Approvals.View()
	v.Pending = pendingInput(in.Control, in.Conversation)
	v.Draft = in.Conversation.DraftView()
	v.PromptDraft = in.Conversation.PromptDraftView()
	v.Admission = m.inputAdmission(in.Control, v.Draft.Available, in.Restoring)
	v.History = m.HistoryView(in.History)
	v.Interrupt = in.Control.InterruptTarget()
	v.Side, v.SideRunning = nil, false
	if in.Conversation.Side != nil {
		side := in.Conversation.Side.View()
		v.Side, v.SideRunning = &side, in.SideRunning
	}
	s := &v.Status
	state, control := m.Status, in.Control
	s.Session = Label{Text: state.Text, Alert: state.Alert, Failure: state.failure, FailureKnown: state.failureKnown,
		Completion: state.completed, CompletionKnown: state.completionKnown}
	s.State = s.Session
	pending := control.Starting() || control.InputPending() || in.Restoring || control.Thread == ""
	s.Animating = in.Processes > 0 || in.SideRunning || !state.Alert && (control.Turn != "" || pending)
	switch {
	case control.Replacement.Archiving():
		s.Session = Label{Text: "Archiving session…", Animation: Pulse}
	case state.Text == "" || state.Alert:
	case control.Turn != "":
		s.Session.Running, s.Session.Animation = true, Pulse
		s.Session.Elapsed, s.Session.ElapsedKnown = now.Sub(state.Started), !state.Started.IsZero()
		if state.Text == "Working" {
			s.Session.Animation = Working
			switch {
			case control.Shell.Standalone():
				s.Session.Text = "Running shell"
			case m.compacting != [2]string{}:
				s.Session.Text = "Compacting context"
			case m.polling != [2]string{}:
				s.Session.Text = "Still running"
			}
		}
	case pending:
		s.Session.Animation = Pulse
	}
	s.Composer = s.Session
	if v.Displayed.ID != v.Main.ID {
		s.Composer = Label{Text: "Read-only"}
		s.ComposerOverride = true
		return v
	}
	if state.Alert {
		s.Details[0] = Label{Text: state.Text, Alert: true, Failure: state.failure, FailureKnown: state.failureKnown,
			Completion: state.completed, CompletionKnown: state.completionKnown}
	}
	if in.Feedback.Alert {
		s.Details[1] = Label{Text: in.Feedback.Text, Alert: true}
	}
	if v.Approvals.Open {
		s.Composer = Label{Text: "approving · turn waiting"}
		s.ComposerOverride = true
	} else if call := v.Questions.Call; call != nil {
		text := "answering"
		if i := v.Questions.Index; i >= 0 && i < len(call.Questions) && call.Questions[i].Note {
			text += " · note"
		}
		if call.Sync || call.Local {
			text += " · turn waiting"
		}
		if in.Conversation.PromptEditor.Parked.Snapshot.Text != "" {
			text += " · draft kept"
		}
		s.Composer = Label{Text: text}
		s.ComposerOverride = true
	}
	s.ShellMode, s.Processes = in.Conversation.ShellMode(), in.Processes
	s.Feedback = Label{Text: in.Feedback.Text, Alert: in.Feedback.Alert}
	return v
}

// ObserveProgress consumes decoded host identity, retaining only active Main's
// compaction and polling keys. Completed comes from Activity's retained source
// record, so duplicate starts need no second completion registry.
// Source: apps/mekugi/internal/router/app_server_progress.go:85:137@1145fbbc.
func (m *Model) ObserveProgress(e observation.Event, activeTurn string, completed bool) {
	if e.ThreadID != m.view.Main.ID {
		return
	}
	switch e.Kind {
	case observation.TurnStarted:
		m.compacting, m.polling = [2]string{}, [2]string{}
	case observation.TurnCompleted:
		if e.TurnID == activeTurn {
			m.compacting, m.polling = [2]string{}, [2]string{}
		}
	case observation.AgentDelta, observation.ReasoningDelta:
		if e.TurnID == activeTurn {
			m.polling = [2]string{}
		}
	case observation.ItemStarted, observation.ItemCompleted:
		if e.TurnID != activeTurn || activeTurn == "" {
			return
		}
		if e.Item.Type != "contextCompaction" {
			if e.Kind == observation.ItemStarted || e.Item.Type == "agentMessage" || e.Item.Type == "commandExecution" && m.polling != [2]string{} && m.polling[1] == e.Item.ProcessID {
				m.polling = [2]string{}
			}
			return
		}
		m.polling = [2]string{}
		key := [2]string{e.TurnID, e.Item.ID}
		if e.Kind == observation.ItemStarted && !completed {
			m.compacting = key
		} else if e.Kind == observation.ItemCompleted && m.compacting == key {
			m.compacting = [2]string{}
		}
	}
}

func (m *Model) TerminalInteraction(thread, turn, activeTurn, process, stdin string) {
	if thread != m.view.Main.ID || turn != activeTurn || activeTurn == "" || process == "" {
		return
	}
	key := [2]string{turn, process}
	if stdin == "" {
		m.polling = key
	} else if m.polling == key {
		m.polling = [2]string{}
	}
}
