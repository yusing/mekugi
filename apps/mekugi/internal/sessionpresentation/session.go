// Package sessionpresentation owns renderer-neutral attached-session display and
// observational history state. It never stores transcript content or sends RPCs.
package sessionpresentation

import (
	"errors"
	"slices"
	"time"

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

// SameAttachment admits an observed revision of the current attachment. The
// operation owner must separately validate its destination and current state.
func (v Version) SameAttachment(current Version) bool {
	return v.Attachment == current.Attachment && v.Revision <= current.Revision
}

type MainInfo struct {
	Skills                                           activitypresentation.SkillSet
	Workspace, Title, BackendVersion, ApprovalCaller string
	Caller                                           string
}

type View struct {
	DisplayedWorkspace string
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
	Interrupt          sessioncontrol.InterruptTarget
	InterruptAvailable bool
	Side               *SideToken
	BTW                *conversationmodel.SideView
	BTWRunning         bool
	SideThread         Thread
	Panel              *PanelView
}

// SourceWorkspace names displayed content without changing Main's input authority.
func (v View) SourceWorkspace() string {
	if v.DisplayedWorkspace != "" {
		return v.DisplayedWorkspace
	}
	return v.Info.Workspace
}

type DisplayIntent struct {
	Version Version
	Thread  Thread
}

var ErrStaleIntent = errors.New("The action's original session or target is unavailable.")
var ErrReplayIntent = errors.New("Replay is read-only.")

// Service owns entry keys and attachment identity. The application owns stores.
type Service struct {
	status     []presentation.Status
	session    scope.Key[Model]
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
	compactionStarted   time.Time
	view                View
	destinationRevision uint64
	ownerRevision       uint64
	replay              bool
	childRuntime        bool
}

func (m *Model) Attach(main Thread, replay bool) {
	if m.view.Main == main && m.replay == replay {
		return
	}
	version := m.view.Version
	version.Revision++
	destination := m.destinationRevision
	owner := m.ownerRevision
	if m.view.Main.ID != main.ID || m.replay != replay {
		owner = version.Revision
	}
	if m.view.Displayed.ID != main.ID || m.view.Input.ID != main.ID {
		destination = version.Revision
	}
	// Identity discovery retains scoped surfaces; replacement uses a fresh scope.
	*m = Model{
		status: m.status, menu: m.menu, destinationRevision: destination, ownerRevision: owner,
		panel: m.panel, panelSequence: m.panelSequence,
		childRuntime: m.childRuntime,
		resume:       m.resume, resumeSequence: m.resumeSequence,
		view: View{Version: version, Main: main, Displayed: main, Input: main}, replay: replay,
	}
}

// BorrowExecutionStatus takes detached execution status without changing this
// view's display, menu, dialogs, history selection or attachment identity.
func (m *Model) BorrowExecutionStatus(source *Model) {
	m.Status = source.Status
	m.compacting, m.polling = source.compacting, source.polling
	m.compactionStarted = source.compactionStarted
}

func (m *Model) View() View {
	v := cloneView(m.view)
	v.ReadOnly = m.readOnly()
	return v
}

// AdmitDestination binds unqualified actions to the uninterrupted input/display
// destination, independently of publication progress and caller-label changes.
func (m *Model) AdmitDestination(version Version) error {
	if m.admitOwner(version) != nil || version.Revision < m.destinationRevision {
		return ErrStaleIntent
	}
	return nil
}

// admitOwner rejects observations of a different execution incarnation, even
// when identity discovery or in-process replacement reuses the presentation.
func (m *Model) admitOwner(version Version) error {
	if !version.SameAttachment(m.view.Version) || version.Revision < m.ownerRevision {
		return ErrStaleIntent
	}
	return nil
}

func (m *Model) readOnly() ReadOnly {
	if m.replay {
		return Replay
	}
	if m.DisplayingChild() && !m.DisplayingSide() {
		return ChildDisplayed
	}
	return Editable
}

// RestrictChildInput keeps prepared children observational until logical admission.
func (m *Model) RestrictChildInput(restricted bool) {
	if m.childRuntime != restricted {
		m.childRuntime = restricted
		m.view.Version.Revision++
	}
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

		v.Side = &side
	}
	v.Caption.Items = slices.Clone(v.Caption.Items)
	if v.BTW != nil {
		body := *v.BTW
		body.Answer = slices.Clone(body.Answer)
		body.History = slices.Clone(body.History)
		for i := range body.History {
			body.History[i].Answer = slices.Clone(body.History[i].Answer)
		}
		v.BTW = &body
	}
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
	return v
}

func (m *Model) Display(intent DisplayIntent) error {
	if m.admitOwner(intent.Version) != nil || intent.Thread.ID == "" {
		return ErrStaleIntent
	}
	for _, current := range []Thread{m.view.Main, m.view.SideThread, m.view.Displayed} {
		if current.ID == intent.Thread.ID {
			intent.Thread = current
			break
		}
	}
	if intent.Thread != m.view.Displayed {
		changed := intent.Thread.ID != m.view.Displayed.ID
		m.view.Displayed = intent.Thread
		m.view.Input = m.view.Main
		if m.DisplayingSide() {
			m.view.Input = intent.Thread
		}
		m.view.Version.Revision++
		if changed {
			m.destinationRevision = m.view.Version.Revision
		}
	}
	return nil
}

func (m *Model) RenameCaller(old, caller string) {
	if old == caller {
		return
	}
	changed := false
	for _, thread := range []*Thread{&m.view.Main, &m.view.Displayed, &m.view.Input, &m.view.SideThread} {
		if thread.Caller == old {
			thread.Caller, changed = caller, true
		}
	}
	if changed {
		m.view.Version.Revision++
	}
}
