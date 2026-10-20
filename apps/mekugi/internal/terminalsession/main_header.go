package terminalsession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Source: apps/mekugi/internal/router/app_server_questions.go:510:527,
// app_server_session_title.go:104:119 and app_server_ui.go:963:979@9fbf51e1.
func (m *Main) titleDetail(focused bool) string {
	var details []string
	if !focused && m.session.Questions.Count > 0 {
		details = append(details, fmt.Sprintf("?%d", m.session.Questions.Count))
	}
	if set := m.session.Info.Skills; set != nil {
		details = append(details, activityui.SkillLabel(set))
	}
	return strings.Join(slices.DeleteFunc(details, func(detail string) bool { return detail == "" }), terminalui.Dim+" · "+terminalui.Undim)
}

func (m *Main) HeaderRight(view *activityui.View, width int, focused bool) string {
	scroll := scrollLabel(view)
	name := m.session.Info.Title
	title := strings.Join(strings.Fields(textsanitize.Safe(name, false)), " ")
	if title == "" {
		return scroll
	}
	// Account for both labels' framing spaces and one separating border cell.
	available := width - 2 - ansi.StringWidth(terminalui.PaneTitle(1, "Main", m.titleDetail(focused), focused)) - 5
	if scroll != "" && available > ansi.StringWidth(scroll)+6 {
		title = ansi.Truncate(title, available-ansi.StringWidth(scroll)-3, "…")
		return scroll + " · " + title
	}
	return ansi.Truncate(title, max(0, available), "…")
}

// scrollLabel shows how much of the feed lies above the viewport and, once
// the reader scrolls back, how much lies below and what arrived since.
func scrollLabel(v *activityui.View) string {
	below := v.FeedLines - v.Offset - v.FeedRows
	switch {
	case !v.Following && below > 0:
		label := fmt.Sprintf("▼ %d", below)
		if v.Unseen > 0 {
			label += fmt.Sprintf(" · %d new", v.Unseen)
		}
		return terminalui.Amber + label + terminalui.Reset
	case v.Offset > 0:
		return terminalui.Dim + fmt.Sprintf("▲ %d", v.Offset) + terminalui.Undim
	}
	return ""
}

// Source: apps/mekugi/internal/router/app_server_history_pages.go:210:223@9fbf51e1.
func (m *Main) historyHint() string {
	for _, h := range m.session.History.Entries {
		if h.Status == sessionpresentation.Loading && !h.Initial {
			return "Loading older · Esc cancels"
		}
	}
	for _, h := range m.session.History.Entries {
		if h.Thread != m.session.History.Target {
			continue
		}
		if h.Status == sessionpresentation.Failed {
			return "History unavailable · Ctrl-B o retry"
		}
		return "Older history · Ctrl-B o load"
	}
	return ""
}
