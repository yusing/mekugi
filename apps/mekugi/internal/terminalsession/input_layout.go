package terminalsession

import (
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Resize interprets prefix navigation and resizes only actual terminal views.
// Source: apps/mekugi/internal/router/terminal_ui.go:139:170@ac13a4fc.
func (in FrameInput) Resize(key string) {
	if key == "\x1b[5~" || key == "\x1b[6~" {
		if in.Shell.Layout.Live.H > 0 {
			caller := in.Main.session.Displayed.Caller
			action := byte('b')
			if key == "\x1b[6~" {
				action = ' '
			}
			in.Live.ScrollBatch(caller, action)
			return
		}
		if key == "\x1b[5~" {
			in.displayedTranscript().ScrollKey('b')
		} else {
			in.displayedTranscript().ScrollKey(' ')
		}
		return
	}
	in.Shell.Panes.Resize(key, in.Width)
	if navigator, ok := in.Shell.Auxiliary.(terminalui.PaneNavigator); ok && (key == "[" || key == "]") {
		columns := navigator.NavigatorColumns()
		if columns == 0 {
			columns = max(25, in.Shell.Layout.Diff.W/4)
		}
		if key == "[" {
			columns -= 2
		} else {
			columns += 2
		}
		navigator.SetNavigatorColumns(max(16, columns))
	}
}

// TerminalColor applies OSC 10/11 default-color reports to every pane, as the
// standalone Activity and diff views did for their own queries.
// Source: apps/mekugi/internal/router/terminal_ui.go:174:201@ac13a4fc.
func (in FrameInput) TerminalColor(reply string) {
	views := []*activityui.View{in.Agents, in.Transcript}
	if fg, ok := diffrender.ForegroundColor(reply); ok {
		for _, view := range views {
			if view != nil {
				view.Painter.Colors.Foreground, view.Painter.Colors.HasForeground = fg, true
			}
		}
	}
	theme, ok := diffrender.BackgroundTheme(reply)
	if !ok {
		return
	}
	bg, _ := diffrender.BackgroundColor(reply)
	for _, view := range views {
		if view != nil {
			view.Painter.Theme = theme
			view.Painter.Colors.Background, view.Painter.Colors.HasBackground = bg, true
		}
	}
	in.Shell.Theme = theme
	if in.Shell.Auxiliary != nil {
		in.Shell.Auxiliary.SetTheme(theme)
	}
}

// Source: apps/mekugi/internal/router/terminal_ui.go:542:549@ac13a4fc.
func (in FrameInput) ClearSkillsHover() {
	for _, view := range []*activityui.View{in.Transcript, in.Agents} {
		if view != nil {
			view.SkillsHover = ""
		}
	}
}
