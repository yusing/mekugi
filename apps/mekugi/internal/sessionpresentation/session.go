// Package sessionpresentation owns renderer-neutral attached-session display and
// observational history state. It never stores transcript content or sends RPCs.
package sessionpresentation

import (
	"errors"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

type Thread struct {
	ID     string
	Caller string
}

type ReadOnly uint8

const (
	Editable ReadOnly = iota
	ChildDisplayed
	Replay
)

type Version struct {
	Attachment uint64
	Revision   uint64
}

type MainInfo struct {
	Skills                                           activitypresentation.SkillSet
	Workspace, Title, BackendVersion, ApprovalCaller string
}

type View struct {
	Info               MainInfo
	Menu               *MenuView
	Resume             *ResumeView
	Version            Version
	Main               Thread
	Displayed          Thread
	Input              Thread
	ReadOnly           ReadOnly
	Loading            string
	Status             MainStatus
	Caption            Caption
	Questions          prompt.QuestionDockView
	Approvals          prompt.ApprovalDockView
	Pending            conversationmodel.PendingView
	Draft              conversationmodel.DraftView
	PromptDraft        conversationmodel.PromptDraftView
	Admission          InputAdmission
	History            HistoryDock
	Interrupt          sessioncontrol.InterruptTarget
	InterruptAvailable bool
	Side               *conversationmodel.SideView
	SideRunning        bool
	Panel              *PanelView
}

type DisplayIntent struct {
	Version Version
	Thread  Thread
}

type HistoryIntent struct {
	Version Version
	Thread  string
}

var ErrStaleIntent = errors.New("The displayed session changed; select the target again.")
var ErrReplayIntent = errors.New("Replay is read-only.")

// Service owns entry keys and attachment identity. The application owns stores.
type Service struct {
	status     []presentation.Status
	session    scope.Key[Model]
	history    scope.Key[History]
	attachment uint64
}

func (s *Service) Session(store *scope.Store) *Model {
	m := s.session.Value(store)
	if m.view.Version.Attachment == 0 {
		m.status = s.status
		s.attachment++
		m.view.Version.Attachment = s.attachment
	}
	return m
}
func (s *Service) History(store *scope.Store) *History { return s.history.Value(store) }

// Source: apps/mekugi/internal/router/native_shell.go:265:283@db279442
type Model struct {
	menu                MenuState
	panel               *PanelState
	panelSequence       uint64
	resume              *ResumeChooser
	resumeSequence      uint64
	status              []presentation.Status
	Status              MainState
	compacting, polling [2]string
	view                View
	lastChild           string
	replay              bool
}

func (m *Model) Attach(main Thread, replay bool) {
	if m.view.Main == main && m.replay == replay {
		return
	}
	version := m.view.Version
	version.Revision++
	// Identity discovery retains scoped surfaces; replacement uses a fresh scope.
	*m = Model{
		status: m.status, menu: m.menu,
		panel: m.panel, panelSequence: m.panelSequence,
		resume: m.resume, resumeSequence: m.resumeSequence,
		view: View{Version: version, Main: main, Displayed: main, Input: main}, replay: replay,
	}
}

func (m *Model) View() View {
	v := cloneView(m.view)
	v.ReadOnly = Editable
	switch {
	case m.replay:
		v.ReadOnly = Replay
	case v.Displayed.ID != v.Main.ID:
		v.ReadOnly = ChildDisplayed
	}
	return v
}

// cloneView keeps both retained publications and subsequent readers isolated
// from mutable values supplied by another owner or a previous consumer.
func cloneView(v View) View {
	v.Info.Skills = slices.Clone(v.Info.Skills)
	if v.Menu != nil {
		menu := *v.Menu
		menu.Choices = slices.Clone(menu.Choices)
		v.Menu = &menu
	}
	if v.Resume != nil {
		resume := *v.Resume
		resume.Rows = slices.Clone(resume.Rows)
		v.Resume = &resume
	}
	if v.Panel != nil {
		panel := *v.Panel
		panel.Fields = slices.Clone(panel.Fields)
		for i := range panel.Fields {
			if value := panel.Fields[i].Remaining; value != nil {
				panel.Fields[i].Remaining = new(*value)
			}
		}
		if panel.Metrics != nil {
			metrics := panel.Metrics.Clone()
			panel.Metrics = &metrics
		}
		v.Panel = &panel
	}

	if v.Side != nil {
		side := *v.Side
		side.Answer = slices.Clone(side.Answer)
		v.Side = &side
	}
	v.Caption.Items = slices.Clone(v.Caption.Items)
	if v.Questions.Call != nil {
		call := *v.Questions.Call
		call.Questions = slices.Clone(call.Questions)
		for i := range call.Questions {
			q := &call.Questions[i]
			q.Choices = slices.Clone(q.Choices)
			q.Answer = slices.Clone(q.Answer)
		}
		v.Questions.Call = &call
	}
	v.Approvals.Approval.Details = slices.Clone(v.Approvals.Approval.Details)
	v.Approvals.Approval.Choices = slices.Clone(v.Approvals.Approval.Choices)
	v.Pending.Waiting = slices.Clone(v.Pending.Waiting)
	v.Pending.Queued = slices.Clone(v.Pending.Queued)
	v.Draft.Draft = v.Draft.Draft.Clone()
	v.PromptDraft.Draft = v.PromptDraft.Draft.Clone()
	v.History.Entries = slices.Clone(v.History.Entries)
	return v
}

func (m *Model) Display(intent DisplayIntent) error {
	if intent.Version != m.view.Version || intent.Thread.ID == "" {
		return ErrStaleIntent
	}
	if intent.Thread != m.view.Displayed {
		m.view.Displayed = intent.Thread
		m.view.Version.Revision++
	}
	if intent.Thread.ID != m.view.Main.ID {
		m.lastChild = intent.Thread.ID
	}
	return nil
}

func (m *Model) RenameCaller(old, caller string) {
	if old == caller {
		return
	}
	changed := false
	for _, thread := range []*Thread{&m.view.Main, &m.view.Displayed, &m.view.Input} {
		if thread.Caller == old {
			thread.Caller, changed = caller, true
		}
	}
	if changed {
		m.view.Version.Revision++
	}
}

// PreferredHistory is independent of the renderer's highlighted roster row.
func (m *Model) PreferredHistory() string {
	if m.view.Displayed.ID != m.view.Main.ID {
		return m.view.Displayed.ID
	}
	return m.lastChild
}

func (m *Model) StartHistory(h *History, thread string) PageRequest {
	*h = History{thread: thread, initial: true, turn: -2}
	m.view.Loading = thread
	m.view.Version.Revision++
	return h.request(TurnMetadata, "", "", "")
}

func (m *Model) LoadHistory(intent HistoryIntent, h *History) (PageRequest, error) {
	if intent.Version != m.view.Version || h == nil || intent.Thread != h.thread {
		return PageRequest{}, ErrStaleIntent
	}
	if m.replay {
		return PageRequest{}, ErrReplayIntent
	}
	if m.view.Loading != "" || !h.Available() {
		return PageRequest{}, nil
	}
	m.view.Loading = h.thread
	m.view.Version.Revision++
	return h.next(), nil
}

func (m *Model) FinishHistory(h *History) bool {
	if m.view.Loading != h.thread {
		return false
	}
	initial := h.initial
	h.initial, h.requestID = false, ""
	m.view.Loading = ""
	m.view.Version.Revision++
	return initial
}

func (m *Model) CancelHistory(intent HistoryIntent, h *History) (string, error) {
	if intent.Version != m.view.Version || h == nil || intent.Thread != h.thread {
		return "", ErrStaleIntent
	}
	if m.replay {
		return "", ErrReplayIntent
	}
	if m.view.Loading != h.thread || h.initial {
		return "", nil
	}
	id := h.requestID
	h.requestID = ""
	return id, nil
}
