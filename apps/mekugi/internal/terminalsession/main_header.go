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

// Header publishes the former pane-label facts without ANSI or width clipping.
// The companion owns native layout; only this renderer knows its scroll state.
func (in FrameInput) Header() sessionpresentation.FrontendHeader {
	v := in.Main.session
	h := sessionpresentation.FrontendHeader{Displayed: v.Displayed, Name: "Main", Skills: slices.Clone(v.Info.Skills)}
	if in.Shell.Focus != 0 && v.Displayed.ID == v.Main.ID && (v.Info.Caller == "" || v.Info.Caller == "/root") {
		h.Questions = v.Questions.Count
	}
	if v.Info.Caller != "" && v.Info.Caller != "/root" {
		h.Name, h.Detail, h.Skills = "Thread", ansi.Strip(in.Agents.Painter.Agent(v.Info.Caller)), nil
	}
	if v.Displayed.ID != v.Main.ID {
		h.Name, h.Detail, h.Skills = "Thread", ansi.Strip(in.Agents.Painter.Agent(v.Displayed.Caller)), nil
		if v.Displayed == v.SideThread && v.Side != nil {
			h.Name, h.Detail = v.Side.Label, ""
		}
	} else {
		h.Title = strings.Join(strings.Fields(textsanitize.Safe(v.Info.Title, false)), " ")
	}
	view := in.displayedTranscript()
	h.Above = max(0, view.Offset)
	if !view.Following {
		h.Below = max(0, view.FeedLines-view.Offset-view.FeedRows)
		if h.Below > 0 {
			h.Unseen = max(0, view.Unseen)
		}
	}
	return h
}

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
