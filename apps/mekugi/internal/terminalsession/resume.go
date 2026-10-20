package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"unicode/utf8"
)

const resumePickerNear = 5

// ResumePicker owns the terminal cursor and row geometry, never host requests.
type ResumePicker struct {
	version                           sessionpresentation.Version
	body                              *sessionpresentation.ResumeView
	main                              string
	Selected, Top, Viewport, RowStart int
}

func (p *ResumePicker) Update(v sessionpresentation.View) {
	if v.Resume == nil {
		*p = ResumePicker{}
		return
	}
	if p.body == nil || p.version.Attachment != v.Version.Attachment || p.body.ID != v.Resume.ID {
		*p = ResumePicker{}
	} else if p.body.Query != v.Resume.Query || p.body.All != v.Resume.All {
		p.Selected, p.Top = 0, 0
	}
	p.body, p.version, p.main = v.Resume, v.Version, v.Main.ID
	p.Selected = max(0, min(p.Selected, len(p.body.Rows)-1))
}
func (p *ResumePicker) Open() bool { return p.body != nil }
func (p *ResumePicker) action(kind sessionpresentation.ResumeActionKind) sessionpresentation.ResumeAction {
	return sessionpresentation.ResumeAction{Version: p.version, Picker: p.body.ID, Kind: kind}
}
func (p *ResumePicker) PageDemand() sessionpresentation.ResumeAction {
	if p.body == nil || !p.body.CanPage || len(p.body.Rows) > max(p.Selected+resumePickerNear, p.Viewport) {
		return sessionpresentation.ResumeAction{}
	}
	action := p.action(sessionpresentation.ResumePage)
	action.After = p.body.PageAfter
	return action
}

// Source: apps/mekugi/internal/router/app_server_resume_picker.go:234:300@630ce384.
func (p *ResumePicker) Key(key string) sessionpresentation.ResumeAction {
	if p.body == nil {
		return sessionpresentation.ResumeAction{}
	}
	v := p.body
	move := func(to int) { p.Selected = max(0, min(len(v.Rows)-1, to)) }
	action := p.action(sessionpresentation.ResumeNoAction)
	switch key {
	case "\x1b":
		if v.Query != "" {
			action.Kind = sessionpresentation.ResumeSearch
		} else {
			action.Kind = sessionpresentation.ResumeDismiss
		}
	case "\x03":
		action.Kind = sessionpresentation.ResumeCancel
	case "\x1b[A", "\x1bOA", "\x10":
		move(p.Selected - 1)
	case "\x1b[B", "\x1bOB", "\x0e":
		move(p.Selected + 1)
	case "\x1b[5~":
		move(p.Selected - max(1, p.Viewport))
	case "\x1b[6~":
		move(p.Selected + max(1, p.Viewport))
	case "\x1b[H", "\x1bOH":
		move(0)
	case "\x1b[F", "\x1bOF":
		move(len(v.Rows) - 1)
	case "\t":
		if v.Workspace != "" {
			action.Kind = sessionpresentation.ResumeToggleWorkspace
		}
	case "\r":
		if len(v.Rows) > 0 {
			action.Kind, action.Thread = sessionpresentation.ResumeChoose, v.Rows[p.Selected].ID
		}
	case "\x7f", "\b":
		if v.Query != "" {
			_, n := utf8.DecodeLastRuneInString(v.Query)
			action.Kind, action.Query = sessionpresentation.ResumeSearch, v.Query[:len(v.Query)-n]
		}
	default:
		if key == "" || key[0] < 32 || key[0] == 0x7f {
			return action
		}
		action.Kind, action.Query = sessionpresentation.ResumeSearch, v.Query+key
	}
	if action.Kind == sessionpresentation.ResumeNoAction {
		return p.PageDemand()
	}
	return action
}
func (p *ResumePicker) Mouse(button, y int) sessionpresentation.ResumeAction {
	if p.body == nil {
		return sessionpresentation.ResumeAction{}
	}
	switch button {
	case 64:
		return p.Key("\x1b[A")
	case 65:
		return p.Key("\x1b[B")
	case 0:
		if row := p.Top + y - p.RowStart; y >= p.RowStart && y-p.RowStart < p.Viewport && row < len(p.body.Rows) {
			p.Selected = row
		}
	}
	return sessionpresentation.ResumeAction{}
}
