// Source: apps/mekugi/internal/sessionpresentation/status.go:1:310@[14a6d1f3]
package sessionpresentation

import (
	"reflect"
	"slices"
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
	FeedbackRevision                   uint64
	State, Session, Composer, Feedback Label
	ComposerOverride                   bool
	Details                            [2]Label
	ShellMode                          bool
	Processes                          int
	Animating                          bool
}

// SetFeedback binds a feedback value and its detail surface together. A local
// overlay uses revision zero so painting cannot acknowledge a covered source.
func (s *MainStatus) SetFeedback(value Label, revision uint64) {
	s.Feedback, s.FeedbackRevision = value, revision
	s.Details[1] = Label{}
	if value.Alert {
		s.Details[1] = value
	}
}

// MainInputs borrows current owner state only for publication. Nothing here is
// retained, and the resulting view contains no owner pointers or capabilities.
type MainInputs struct {
	Info                   MainInfo
	Menu                   *MenuView
	Control                *sessioncontrol.Live
	RecordedTurn           string // Playback selection, used only without live control.
	Feedback               *noticepresentation.Model
	Conversation           *conversationmodel.Session
	ClientEditors          bool // Editor contents remain client-owned, outside this view's retained publication.
	Restoring, SideRunning bool
	BTWRunning             bool
	SideThreads            []string
	Processes              int
	History                []HistoryView
}

type Caption struct {
	Model, Effort, Tier, Role    string
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
		if in.ClientEditors {
			v.Draft = conversationmodel.DraftView{Available: v.Draft.Available, Prediction: v.Draft.Prediction}
			v.PromptDraft = conversationmodel.PromptDraftView{Call: v.PromptDraft.Call, Question: v.PromptDraft.Question, Request: v.PromptDraft.Request}
		}
		if !reflect.DeepEqual(v, m.view) {
			v.Version.Revision++
			m.view = cloneView(v)
		}
		v = m.View()
	}()
	v.Info = in.Info
	v.Caption = caption
	v.Panel = m.panel.View(in.Info.Workspace)
	v.Resume = m.resume.View()
	v.Menu = in.Menu
	thread, turn := v.Main.ID, in.RecordedTurn
	pending, archiving, shell := false, false, false
	v.Pending = conversationmodel.PendingView{}
	v.Interrupt = sessioncontrol.InterruptTarget{}
	v.InterruptAvailable = false
	if control := in.Control; control != nil {
		thread, turn = control.Thread, control.Turn
		pending = control.Starting() || control.InputPending() || in.Restoring || thread == ""
		archiving, shell = control.Replacement.Archiving(), control.Shell.Standalone()
		v.Pending = pendingInput(control, in.Conversation)
		v.Interrupt = control.InterruptTarget()
		v.InterruptAvailable = MainInterruptible(control, in.Conversation)
	}
	v.Questions = in.Conversation.Questions.View(thread)
	if side := in.Conversation.Side; side != nil && side.OneOff {
		for _, thread := range in.SideThreads {
			v.Questions.Count += in.Conversation.Questions.Count(thread)
		}
	}
	v.Approvals = in.Conversation.Approvals.View()
	v.Draft = in.Conversation.DraftView()
	v.PromptDraft = in.Conversation.PromptDraftView()
	v.Admission = m.inputAdmission(in.Control, v.Draft.Available, in.Restoring)
	v.History = m.HistoryView(in.History)
	v.Side = nil
	v.BTW, v.BTWRunning = nil, false
	if body := in.Conversation.BTW; body != nil && v.Displayed == v.Main {
		v.BTW = &conversationmodel.SideView{Key: body.Key, History: body.History, ID: body.ID, Question: body.Question, Status: body.Status, Answer: body.Answer, Alert: body.Alert, Truncated: body.Truncated, AttachmentNotice: body.AttachmentNotice, Started: body.Started}
		v.BTWRunning = in.BTWRunning
	}
	if in.Conversation.Side != nil {
		v.Side = &SideToken{ID: in.Conversation.Side.ID, Key: in.Conversation.Side.Key, Label: in.Conversation.Side.Label()}
	}
	state := m.Status
	base := Label{Text: state.Text, Alert: state.Alert, Failure: state.failure, FailureKnown: state.failureKnown,
		Completion: state.completed, CompletionKnown: state.completionKnown}
	v.Status.Session = m.sessionLabel(base, turn, pending, archiving, shell, now)
	if m.DisplayingSide() && in.Conversation.Side != nil {
		side := in.Conversation.Side
		v.Draft = side.Drafts.DraftView()
		v.PromptDraft = side.Drafts.PromptDraftView()
		v.Questions = side.Drafts.Questions.View(v.SideThread.ID)
		v.Approvals = side.Drafts.Approvals.View()
		if side.OneOff {
			for _, thread := range in.SideThreads {
				v.Questions.Count += in.Conversation.Questions.Count(thread)
			}
			for _, approval := range in.Conversation.Approvals.Pending {
				if slices.Contains(in.SideThreads, approval.Thread) {
					v.Approvals.Count++
				}
			}
		}
		v.Pending = side.Drafts.Pending(nil, conversationmodel.Waiting)
		if len(v.Pending.Queued) > 0 {
			v.Pending.Reason = conversationmodel.RetryAttachments
			v.Pending.Waiting = append(v.Pending.Waiting, v.Pending.Queued...)
			v.Pending.Queued = nil
		}
		v.Interrupt, v.InterruptAvailable = sessioncontrol.InterruptTarget{}, false
		v.Admission = m.inputAdmission(in.Control, true, in.Restoring)
		v.Status.State = Label{Text: side.Status, Alert: side.Alert, Running: in.SideRunning}
		v.Status.Composer = v.Status.State
		if in.SideRunning {
			v.Status.Composer.Animation = Pulse
			v.Status.Composer.Elapsed = now.Sub(side.Started)
			v.Status.Composer.ElapsedKnown = !side.Started.IsZero()
		}
		v.Status.Animating = in.SideRunning || v.BTWRunning
		v.Status.FeedbackRevision = in.Feedback.Revision
		v.Status.Feedback = Label{Text: in.Feedback.Text, Alert: in.Feedback.Alert}
		return v
	}
	s := &v.Status
	s.State = base
	s.Animating = in.Processes > 0 || in.SideRunning || v.BTWRunning || !state.Alert && (turn != "" || pending)
	v.ProjectComposerStatus(in.Conversation.PromptEditor.Parked.Snapshot.Text != "")
	if v.Displayed.ID != v.Main.ID {
		return v
	}
	if state.Alert {
		s.Details[0] = Label{Text: state.Text, Alert: true, Failure: state.failure, FailureKnown: state.failureKnown,
			Completion: state.completed, CompletionKnown: state.completionKnown}
	}
	s.ShellMode, s.Processes = in.Conversation.ShellMode(), in.Processes
	s.SetFeedback(Label{Text: in.Feedback.Text, Alert: in.Feedback.Alert}, in.Feedback.Revision)
	return v
}

// ProjectComposerStatus selects the composer label from published session facts
// and the current prompt/editor presentation, including client-local overlays.
func (v *View) ProjectComposerStatus(draftKept bool) {
	if v.SideThread.ID != "" && v.Displayed == v.SideThread {
		return // The side conversation publishes its own status and timing.
	}
	s := &v.Status
	s.Composer, s.ComposerOverride = s.Session, false
	if v.Displayed.ID != v.Main.ID {
		s.Composer, s.ComposerOverride = Label{Text: "Read-only"}, true
		return
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
		if draftKept {
			text += " · draft kept"
		}
		s.Composer = Label{Text: text}
		s.ComposerOverride = true
	}
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

// sessionLabel is Main's own status, whichever thread is displayed.
func (m *Model) sessionLabel(l Label, turn string, pending, archiving, shell bool, now time.Time) Label {
	state := m.Status
	switch {
	case archiving:
		l = Label{Text: "Archiving session…", Animation: Pulse}
	case state.Text == "" || state.Alert:
	case turn != "":
		l.Running, l.Animation = true, Pulse
		l.Elapsed, l.ElapsedKnown = now.Sub(state.Started), !state.Started.IsZero()
		if state.Text == "Working" {
			l.Animation = Working
			switch {
			case shell:
				l.Text = "Running shell"
			case m.compacting != [2]string{}:
				l.Text = "Compacting context"
			case m.polling != [2]string{}:
				l.Text = "Still running"
			}
		}
	case pending:
		l.Animation = Pulse
	}
	return l
}

// MainInterruptible reports whether Main has work an interruption stops,
// whichever thread is displayed.
func MainInterruptible(control *sessioncontrol.Live, conversation *conversationmodel.Session) bool {
	return control.Turn != "" || control.Starting() || control.InputPending() || control.Compaction.Pending() || control.Compaction.ContinuationPending() || len(conversation.Unsent)+len(conversation.Queued) > 0
}
