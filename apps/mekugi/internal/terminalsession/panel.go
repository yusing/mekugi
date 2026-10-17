package terminalsession

import "github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

// StatusPanel owns navigation and geometry over one detached publication.
type StatusPanel struct {
	version         sessionpresentation.Version
	body            *sessionpresentation.PanelView
	Rect            Rect
	Top, Rows, Tab  int
	Sequence        uint64 // Zero follows the newest retained exchange.
	Title, Controls string
	Groups          []string
	Fields          []sessionpresentation.StatusField
}

func (p *StatusPanel) Update(view sessionpresentation.View) {
	if view.Panel == nil {
		*p = StatusPanel{}
		return
	}
	if p.body == nil || p.version.Attachment != view.Version.Attachment || p.body.ID != view.Panel.ID {
		*p = StatusPanel{}
	}
	p.version, p.body = view.Version, view.Panel
	p.Title, p.Controls, p.Groups, p.Fields = "", "", nil, view.Panel.Fields
	if p.body.Metrics != nil {
		p.renderMetrics()
	}
}
func (p *StatusPanel) Open() bool { return p.body != nil }

// Key consumes terminal input, returning only source-identified semantic effects.
// Source: apps/mekugi/internal/router/app_server_status.go:287:314 and
// app_server_session_metrics.go:48:95@630ce384.
func (p *StatusPanel) Key(key string) (action sessionpresentation.PanelAction, clearSelection bool) {
	if p.body == nil {
		return action, false
	}
	action.Version, action.Panel = p.version, p.body.ID
	if p.body.Metrics != nil {
		switched := true
		switch key {
		case "\x1b[C", "\t":
			p.Tab = (p.Tab + 1) % len(metricViews)
		case "\x1b[D", "\x1b[Z":
			p.Tab = (p.Tab + len(metricViews) - 1) % len(metricViews)
		case "[", "]":
			exchanges := p.body.Metrics.Exchanges
			if p.Tab != 2 || len(exchanges) == 0 {
				return action, false
			}
			index := p.exchangeIndex()
			if key == "[" {
				index = max(0, index-1)
			} else {
				index = min(len(exchanges)-1, index+1)
			}
			p.Sequence = exchanges[index].Sequence
			if index == len(exchanges)-1 {
				p.Sequence = 0
			}
		case "r":
			action.Kind = sessionpresentation.PanelRefresh
			return action, false
		default:
			switched = false
		}
		if switched {
			p.Top = 0
			p.renderMetrics()
			return action, true
		}
	}
	switch key {
	case "\x1b", "q", "\r", "\x03":
		action.Kind = sessionpresentation.PanelClose
		clearSelection = true
	case "\x1b[A":
		p.Top = max(0, p.Top-1)
	case "\x1b[B":
		p.Top++
	case "\x1b[5~":
		p.Top = max(0, p.Top-max(1, p.Rows))
	case "\x1b[6~":
		p.Top += max(1, p.Rows)
	case "\x1b[H", "\x1b[1~":
		p.Top = 0
	}
	return action, clearSelection
}
func (p *StatusPanel) exchangeIndex() int {
	if p.Sequence != 0 {
		for i, e := range p.body.Metrics.Exchanges {
			if e.Sequence == p.Sequence {
				return i
			}
		}
		p.Sequence = 0
	}
	return len(p.body.Metrics.Exchanges) - 1
}
