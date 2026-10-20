package sessionpresentation

import (
	"slices"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// PanelView contains source values, never layout or a request handle.
type PanelView struct {
	ID                        uint64
	Fields                    []StatusField
	Metrics                   *transportcapture.MetricsSnapshot
	Workspace, DebugDirectory string
}

// StatusField preserves numeric observations independently of formatted labels.
type StatusField struct {
	Group, Label, Value, Detail string
	Remaining                   *float64
	Alert, Path                 bool
}

type PanelActionKind uint8

const (
	PanelNoAction PanelActionKind = iota
	PanelClose
	PanelRefresh
)

type PanelAction struct {
	Version Version
	Panel   uint64
	Kind    PanelActionKind
}

// AdmitPanel resolves the originating publication before any application effect.
// These observational actions remain available during read-only playback.
func (m *Model) AdmitPanel(in PanelAction) error {
	if in.Version != m.view.Version || m.view.Panel == nil || in.Panel != m.view.Panel.ID {
		return ErrStaleIntent
	}
	return nil
}

// PanelState retains structured presentation, independently of account requests
// and refresh timing. Publication detaches these values with the session view.
// Source: apps/mekugi/internal/router/app_server_status.go:25:45,124:127@0ce86dd5.
type PanelState struct {
	PanelView
	Body, Tokens, Account []StatusField
}

func (m *Model) OpenPanel(debugDirectory string) *PanelState {
	m.panelSequence++
	m.panel = &PanelState{PanelView: PanelView{ID: m.panelSequence, DebugDirectory: debugDirectory}}
	return m.panel
}

func (m *Model) ClosePanel() { m.panel = nil }

func (p *PanelState) Render(extra []StatusField) { p.Fields = slices.Concat(p.Body, p.Account, extra) }

func (p *PanelState) View(workspace string) *PanelView {
	if p == nil {
		return nil
	}
	view := p.PanelView
	view.Workspace = workspace
	return &view
}
