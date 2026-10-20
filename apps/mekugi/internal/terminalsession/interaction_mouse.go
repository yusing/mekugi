package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Source: apps/mekugi/internal/router/terminal_ui.go:269:474@d16ed250.
func (in FrameInput) mouse(state *Interaction, pointer Mouse, yield func(InputEffect) bool) {
	button, x, y, release := pointer.Button, pointer.X, pointer.Y, pointer.Release
	main, shell := in.Main, in.Shell
	if main != nil {
		in.Transcript.SkillsHover = ""
		if button == 35 && !release && in.Dialog == nil && shell.Layout.MainSkills.Contains(x, y) {
			in.Transcript.SkillsHover = "Main"
		}
	}
	if in.Agents != nil && (in.Dialog != nil || !shell.Layout.Roster.Contains(x, y)) {
		in.Agents.SkillsHover = ""
	}
	if in.Dialog != nil {
		intent := in.Dialog.Mouse(button, x, y, release)
		yield(InputEffect{Dialog: &intent})
		return
	}
	if state.Selection == nil && !shell.Dragging() {
		for pane, rect := range shell.Tabs {
			if rect.Contains(x, y) {
				if button == 0 && !release {
					state.Prefix = false
					in.selectPane(state, pane, yield)
				}
				return
			}
		}
	}
	if main != nil && main.session.ReadOnly == sessionpresentation.Editable && main.Panel.Open() && shell.Layout.Main.Contains(x, y) {
		if in.selectionMouse(state, pointer, yield) {
			return
		}
		if !release {
			var action sessionpresentation.PanelAction
			switch button &^ 28 {
			case 0:
				shell.Focus = 0
			case 64:
				action, _ = main.Panel.Key("\x1b[A")
			case 65:
				action, _ = main.Panel.Key("\x1b[B")
			default:
				return
			}
			if button&^28 != 0 {
				yield(InputEffect{Panel: &action})
			}
		}
		return
	}
	if main != nil && main.session.ReadOnly == sessionpresentation.Editable && main.Resume.Open() && shell.Layout.Main.Contains(x, y) {
		if release {
			return
		}
		if button&^28 == 0 {
			shell.Focus = 0
		}
		action := main.Resume.Mouse(button&^28, y-shell.Layout.Main.Y)
		yield(InputEffect{Resume: &action})
		return
	}
	if main != nil && in.Prompts.Rect.Contains(x-shell.Layout.Main.X, y-shell.Layout.Main.Y) {
		if !release && button&^28 == 0 {
			yield(InputEffect{PendingPrompt: &PendingPromptInput{Version: main.session.Version}})
		}
		return
	}
	if main != nil && shell.Focus == 0 && main.session.ReadOnly == sessionpresentation.Editable && main.Menu.Open() && main.Menu.Rect.Contains(x-shell.Layout.Main.X, y-shell.Layout.Main.Y) {
		if !release {
			switch button &^ 28 {
			case 0, 64, 65:
				result := main.Menu.Mouse(button&^28, y-shell.Layout.Main.Y)
				if yield(InputEffect{Menu: &result}) {
					in.send(state, "\r", yield)
				}
			}
		}
		return
	}
	if in.selectionMouse(state, pointer, yield) {
		return
	}
	if state.Selection == nil && !shell.Dragging() && main != nil {
		details, dismiss := main.NoticeDetails.Contains(x-shell.Layout.Main.X, y-shell.Layout.Main.Y), main.NoticeDismiss.Contains(x-shell.Layout.Main.X, y-shell.Layout.Main.Y)
		if text := MainStatusDetails(main.session.Status); (details || dismiss) && text != "" {
			if !release && button == 0 {
				yield(InputEffect{Notice: &NoticeInput{Version: main.session.Version, Feedback: main.session.Status.FeedbackRevision, Dismiss: dismiss, Text: text}})
			}
			return
		}
	}
	if main != nil && main.Side.Open() && !release && button&64 != 0 {
		r := main.Side.Rect
		r.X += shell.Layout.Main.X
		r.Y += shell.Layout.Main.Y
		if r.Contains(x, y) {
			delta := 3
			if button&1 != 0 {
				delta = -delta
			}
			main.Side.Scroll = max(0, main.Side.Scroll+delta)
			return
		}
	}
	navigatorWidth := 0
	navigator, hasNavigator := shell.Auxiliary.(terminalui.PaneNavigator)
	if hasNavigator {
		navigatorWidth = navigator.NavigatorWidth(shell.Layout.Diff.W)
	}
	if handled, columns := shell.Panes.Drag(pointer, shell.Layout, navigatorWidth); handled {
		if columns != 0 && hasNavigator {
			navigator.SetNavigatorColumns(columns)
		}
		return
	}
	if shell.Layout.Live.Contains(x, y) {
		if !release {
			switch button &^ 28 {
			case 0:
				shell.Focus = 0
			case 64:
				in.Live.ScrollBatch(main.session.Displayed.Caller, terminalui.PaneWheelUp)
			case 65:
				in.Live.ScrollBatch(main.session.Displayed.Caller, terminalui.PaneWheelDown)
			}
		}
		return
	}
	if button&^28 == 0 && !release && main != nil && shell.Layout.MainSkills.Contains(x, y) {
		shell.Focus = 0
		yield(InputEffect{Details: &DetailInput{Version: main.session.Version, View: in.Transcript, Kind: SkillDetails, Caller: "Main"}})
		return
	}
	pane := -1
	var r Rect
	switch {
	case shell.Layout.Main.Contains(x, y):
		pane, r = 0, shell.Layout.Main
	case shell.Layout.Diff.Contains(x, y):
		pane, r = 1, shell.Layout.Diff
	case shell.Layout.Roster.Contains(x, y):
		pane, r = 3, shell.Layout.Roster
	}
	if pane < 0 {
		return
	}
	if pane != 1 && shell.Auxiliary != nil {
		shell.Auxiliary.ClearHover()
	}
	if button&^28 == 0 && !release {
		shell.Focus = pane
	}
	if release {
		return
	}
	action := byte(0)
	switch button &^ 28 {
	case 0:
		action = '\r'
	case 35:
		action = 'h'
	case 64:
		action = 'k'
	case 65:
		action = 'j'
	}
	switch pane {
	case 0:
		view := in.displayedTranscript()
		view.HandleMouse(action, y-r.Y+1, x-r.X+1)
		if snippet := view.Opening; snippet != (activityui.Snippet{}) {
			view.Opening = activityui.Snippet{}
			v, source := main.session.Version, state.Frame.Source
			v.Attachment = source.Attachment
			yield(InputEffect{Selection: &SelectionInput{Target: SelectionTarget{Version: v, Thread: source.Thread, Workspace: source.Workspace, View: view, Snippet: snippet}}})
		}
	case 1:
		if shell.Auxiliary != nil {
			shell.Auxiliary.Mouse(action, y-r.Y+1, x-r.X+1)
		}
	case 3:
		if action == 'j' {
			in.Agents.ScrollRoster(1)
		} else if action == 'k' {
			in.Agents.ScrollRoster(-1)
		} else {
			child, selected := main.session.Displayed.ID != main.session.Main.ID, in.Agents.Selected
			in.Agents.PointAgent(action, y-r.Y+1, x-r.X+1)
			if agent := in.Agents.SkillsRequest; agent != "" {
				in.Agents.SkillsRequest = ""
				if !yield(InputEffect{Details: &DetailInput{Version: main.session.Version, View: in.Agents, Kind: SkillDetails, Caller: agent}}) {
					return
				}
			}
			in.rosterPick(state, child, selected, yield)
		}
	}
}
