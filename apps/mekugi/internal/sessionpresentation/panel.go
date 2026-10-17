package sessionpresentation

import transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

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
