package sessionpresentation

import "time"

type ResumeRow struct {
	ID, Title, Workspace, Branch string
	Updated                      int64
}

// ResumeView publishes only admitted source facts and stable page targets.
type ResumeView struct {
	ID                                      uint64
	Workspace, Query, Problem               string
	Startup, All, Loading, HasMore, CanPage bool
	Rows                                    []ResumeRow
	Scanned                                 int
	PageAfter                               string
	LoadedAt                                time.Time
}
type ResumeActionKind uint8

const (
	ResumeNoAction ResumeActionKind = iota
	ResumeSearch
	ResumeToggleWorkspace
	ResumeChoose
	ResumeDismiss
	ResumeCancel
	ResumePage
)

type ResumeAction struct {
	Version              Version
	Picker               uint64
	Kind                 ResumeActionKind
	Query, Thread, After string
}

func (m *Model) AdmitResume(in ResumeAction) error {
	p := m.view.Resume
	if in.Version != m.view.Version || p == nil || in.Picker != p.ID {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	switch in.Kind {
	case ResumeChoose:
		for _, row := range p.Rows {
			if row.ID == in.Thread {
				return nil
			}
		}
		return ErrStaleIntent
	case ResumePage:
		if !p.CanPage || in.After != p.PageAfter {
			return ErrStaleIntent
		}
	}
	return nil
}
