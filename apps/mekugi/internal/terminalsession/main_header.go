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
		h.Title = sessionTitle(v.Info.Title)
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

func (in FrameInput) rosterThreadLabel() string {
	v := in.Main.session
	if v.Displayed.ID != v.Main.ID {
		if v.Displayed == v.SideThread && v.Side != nil {
			return v.Side.Label
		}
		return in.Agents.Painter.Agent(v.Displayed.Caller)
	}
	if v.Info.Caller != "" && v.Info.Caller != "/root" {
		return in.Agents.Painter.Agent(v.Info.Caller)
	}
	if in.Shell.Focus != 0 && v.Questions.Count > 0 {
		return fmt.Sprintf("Main ?%d", v.Questions.Count)
	}
	return "Main"
}

func sessionTitle(title string) string {
	return strings.Join(strings.Fields(textsanitize.Safe(title, false)), " ")
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
