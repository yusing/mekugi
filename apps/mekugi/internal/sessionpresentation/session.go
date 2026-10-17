// Package sessionpresentation owns renderer-neutral attached-session display and
// observational history state. It never stores transcript content or sends RPCs.
package sessionpresentation

import (
	"errors"

	"github.com/yusing/mekugi/contracts/scope"
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

type View struct {
	Version   Version
	Main      Thread
	Displayed Thread
	Input     Thread
	ReadOnly  ReadOnly
	Loading   string
	Status    MainStatus
	Caption   Caption
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

// Service owns entry keys and attachment identity. The application owns stores.
type Service struct {
	session    scope.Key[Model]
	history    scope.Key[History]
	attachment uint64
}

func (s *Service) Session(store *scope.Store) *Model {
	m := s.session.Value(store)
	if m.view.Version.Attachment == 0 {
		s.attachment++
		m.view.Version.Attachment = s.attachment
	}
	return m
}
func (s *Service) History(store *scope.Store) *History { return s.history.Value(store) }

// Source: apps/mekugi/internal/router/native_shell.go:265:283@db279442
type Model struct {
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
	*m = Model{view: View{Version: version, Main: main, Displayed: main, Input: main}, replay: replay}
}

func (m *Model) View() View {
	v := m.view
	switch {
	case m.replay:
		v.ReadOnly = Replay
	case v.Displayed.ID != v.Main.ID:
		v.ReadOnly = ChildDisplayed
	}
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
	if intent.Version != m.view.Version || intent.Thread != h.thread {
		return PageRequest{}, ErrStaleIntent
	}
	if m.replay || m.view.Loading != "" || !h.Available() {
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
	if intent.Version != m.view.Version || intent.Thread != h.thread {
		return "", ErrStaleIntent
	}
	if m.view.Loading != h.thread || h.initial {
		return "", nil
	}
	id := h.requestID
	h.requestID = ""
	return id, nil
}
