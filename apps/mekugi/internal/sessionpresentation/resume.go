package sessionpresentation

import (
	"strings"
	"time"
)

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
	if !in.Version.SameAttachment(m.view.Version) || p == nil || in.Picker != p.ID {
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

// ResumeChooser owns the attached chooser's source values and paging policy.
// Source: apps/mekugi/internal/router/app_server_resume_picker.go:24:54,201:213@0ce86dd5.
type ResumeChooser struct {
	ID        uint64
	Startup   bool   // No session exists yet; dismissal starts one and cancellation quits.
	Workspace string // Workspace filter; empty when unavailable.
	All       bool
	Query     string
	Rows      []ResumeRow
	Scanned   int
	seen      map[string]bool
	Cursor    string
	Loading   bool
	Problem   string
	LoadedAt  time.Time
}

func (p *ResumeChooser) Matches() []ResumeRow {
	query := strings.ToLower(strings.TrimSpace(p.Query))
	if query == "" {
		return p.Rows
	}
	var rows []ResumeRow
	for _, row := range p.Rows {
		for _, field := range []string{row.Title, row.ID, row.Branch, row.Workspace} {
			if strings.Contains(strings.ToLower(field), query) {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows
}

func (m *Model) OpenResume(startup bool, workspace string) *ResumeChooser {
	m.resumeSequence++
	m.resume = &ResumeChooser{ID: m.resumeSequence, Startup: startup, Workspace: workspace, All: workspace == "", seen: make(map[string]bool)}
	return m.resume
}

func (m *Model) CloseResume() { m.resume = nil }

// MoveResume transfers the actual startup chooser to its first attached view.
// No rows, paging identity or acquisition are copied or restarted.
func (m *Model) MoveResume(target *Model) {
	if m.resume == nil || target == m {
		return
	}
	target.resume, m.resume = m.resume, nil
	target.resumeSequence = max(target.resumeSequence, target.resume.ID)
}

func (p *ResumeChooser) Reset(now time.Time) {
	p.Rows, p.seen, p.Cursor = nil, make(map[string]bool), ""
	p.Scanned = 0
	p.LoadedAt = now
}

func (p *ResumeChooser) Sent() { p.Loading, p.Problem = true, "" }

func (p *ResumeChooser) Fail(problem string) {
	p.Loading, p.Cursor, p.Problem = false, "", problem
}

func (p *ResumeChooser) Append(rows []ResumeRow, cursor string, scanned int) {
	p.Scanned += scanned
	for _, row := range rows {
		if row.ID == "" || p.seen[row.ID] {
			continue
		}
		p.seen[row.ID] = true
		p.Rows = append(p.Rows, row)
	}
	p.Loading, p.Cursor = false, cursor
}

func (p *ResumeChooser) View() *ResumeView {
	if p == nil {
		return nil
	}
	v := &ResumeView{ID: p.ID, Workspace: p.Workspace, Query: p.Query, Problem: p.Problem,
		Startup: p.Startup, All: p.All, Loading: p.Loading, HasMore: p.Cursor != "", Rows: p.Matches(), Scanned: p.Scanned, LoadedAt: p.LoadedAt}
	v.CanPage = v.HasMore && !p.Loading && (p.Query == "" || p.Scanned < 1000)
	if len(p.Rows) > 0 {
		v.PageAfter = p.Rows[len(p.Rows)-1].ID
	}
	return v
}
