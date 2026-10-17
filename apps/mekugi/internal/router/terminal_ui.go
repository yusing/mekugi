package router

import (
	"strings"
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

// The native shell owns terminal presentation; Codex app-server owns execution.
type terminalUI struct {
	selection     *terminalSelection
	frame         terminalsession.Frame
	input         terminalsession.Decoder
	output        *terminalsession.ContentDialog // Open over both panes, taking every key.
	diffScreen    *terminalsession.DiffScreen
	preparePane   func(*scope.Store)
	agents        *activityui.View
	main          *appServerUI
	width, height int
	terminalsession.Shell
	diffUnseen  bool // Saved changes arrived while Diff was closed.
	liveDock    previewview.PreviewPane
	livePending map[string]nativePendingPreview
	liveHidden  bool // Presentation only, local to this frontend session.
	prefix      bool
	faint       bool // Output capability is local to this terminal, not request or global state.
}

// Decode once before routing. In particular, mouse reports and bracketed paste
// must never leak their payload into pane commands or layout shortcuts.
func (u *terminalUI) key(key byte) error {
	if u.output != nil {
		u.prefix = false
	}
	event := u.input.Feed(key, time.Now())
	if event.Kind != terminalsession.NoInput && u.main != nil {
		defer u.main.settlePresentation()
	}
	switch event.Kind {
	case terminalsession.NoInput:
		return nil
	case terminalsession.PasteStart, terminalsession.PasteInput, terminalsession.PasteEnd:
		if event.Kind == terminalsession.PasteStart {
			u.prefix = false
		}
		if u.main == nil {
			return nil
		}
		if u.output != nil || u.Focus != 0 || u.main.statusPanel != nil || u.main.resumePicker != nil || u.main.composerReadOnly() {
			u.main.pasted = nil
			return nil
		}
		if event.Kind == terminalsession.PasteStart && u.selection != nil && !u.selection.Dragging() {
			u.selection = nil
		}
		_, err := u.main.input(event)
		return err
	case terminalsession.ColorInput:
		u.terminalColor(event.Text)
		return nil
	case terminalsession.FocusInput:
		if u.main != nil && u.main.notifications != nil {
			u.main.notifications.focused = event.Text == "\x1b[I"
		}
		if event.Text == "\x1b[O" {
			u.clearSkillsHover()
		}
		return nil
	case terminalsession.MouseInput:
		if u.main != nil && u.main.keybindings {
			u.main.keybindings = false
			return nil
		}
		return u.mouse(event.Text)
	}
	if strings.HasPrefix(event.Text, "\x1b") {
		if u.prefix {
			u.prefix = false
			u.resize(event.Text)
			return nil
		}
		return u.send(event.Text)
	}
	if u.output == nil && u.selectionKey(event.Text) {
		return nil
	}
	if u.output != nil {
		return u.send(event.Text)
	}
	if u.prefix {
		u.prefix = false
		switch key {
		case 2:
			return u.send("\x02")
		case '1', '2':
			u.selectNativePane(int(key - '1'))
		case '3':
			u.selectNativePane(3)
		case 'o':
			view := u.main.sessionPresentation().View()
			return u.main.loadOlderActivity(sessionpresentation.HistoryIntent{Version: view.Version, Thread: view.History.Target})
		case 'q':
			if u.main != nil && u.main.composerReadOnly() {
				u.selectNativePane(0)
			}
			if u.main != nil && !u.main.openApprovals() {
				u.main.openQuestions()
			}
		case '!':
			u.openErrors()
		case 'e':
			u.liveDock.NextBatch(u.main.transcriptCaller())
		case 'r':
			u.liveDock.ScrollBatch(u.main.transcriptCaller(), 'r')
		default:
			u.resize(string(key))
		}
		return nil
	}
	if key == 2 {
		if u.main != nil {
			u.main.keybindings = false
		}
		u.prefix = true
		return nil
	}
	return u.send(event.Text)
}

func (u *terminalUI) resize(key string) {
	if key == "\x1b[5~" || key == "\x1b[6~" {
		if u.Layout.Live.H > 0 {
			caller := u.main.transcriptCaller()
			action := byte('b')
			if key == "\x1b[6~" {
				action = ' '
			}
			u.liveDock.ScrollBatch(caller, action)
			return
		}
		if key == "\x1b[5~" {
			u.main.transcriptView().ScrollKey('b')
		} else {
			u.main.transcriptView().ScrollKey(' ')
		}
		return
	}
	u.Panes.Resize(key, u.width)
	if navigator, ok := u.Auxiliary.(terminalui.PaneNavigator); ok && (key == "[" || key == "]") {
		columns := navigator.NavigatorColumns()
		if columns == 0 {
			columns = max(25, u.Layout.Diff.W/4)
		}
		if key == "[" {
			columns -= 2
		} else {
			columns += 2
		}
		navigator.SetNavigatorColumns(max(16, columns))
	}
}

// terminalColor applies OSC 10/11 default-color reports to every pane, as the
// standalone Activity and diff views did for their own queries.
func (u *terminalUI) terminalColor(reply string) {
	views := []*activityui.View{u.agents}
	if u.main != nil {
		views = append(views, u.main.view)
	}
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
	u.Theme = theme
	if u.Auxiliary != nil {
		u.Auxiliary.SetTheme(theme)
	}
}

func (u *terminalUI) send(s string) error {
	if u.output != nil {
		u.outputKey(s)
		return nil
	}
	if s != "\x1b" && u.selectionKey(s) {
		return nil
	}
	if s == "\x1b" && u.main != nil && u.main.historyLoading() != nil {
		view := u.main.sessionPresentation().View()
		return u.main.cancelOlderActivity(sessionpresentation.HistoryIntent{Version: view.Version, Thread: view.Loading})
	}
	if u.main != nil && u.Focus == 0 && u.main.composerReadOnly() {
		transcript := u.main.transcriptView()
		switch s {
		case "\x05":
			transcript.ToggleExpansion()
		case "\x1b[5~":
			transcript.ScrollKey('b')
		case "\x1b[6~":
			transcript.ScrollKey(' ')
		case "\x1b":
			if !u.selectionKey(s) {
				transcript.Follow()
			}
		}
		return nil
	}
	if u.main != nil && u.Focus == 0 && u.main.statusPanel != nil {
		if u.selection != nil && !u.selection.Dragging() {
			u.selection = nil
			if s == "\x1b" {
				return nil
			}
		}
		u.main.statusPanelKey(s)
		return nil
	}
	if u.main != nil && u.Focus == 0 && u.main.resumePicker != nil {
		return u.main.resumePickerKey(s)
	}

	if s == "\x1b" && u.main != nil && u.Focus == 0 {
		if handled, err := u.main.approvalKey(s); handled {
			return err
		}
		if handled, err := u.main.questionKey(s); handled {
			return err
		}
	}
	if s == "\x1b" && u.main != nil && u.Focus == 0 && u.main.keybindings {
		u.main.keybindings = false
		return nil
	}
	if s == "\x1b" && u.main != nil && u.Focus == 0 && u.main.pickerKey(s) {
		return nil
	}
	if u.main != nil && u.Focus == 0 {
		if s == "\x1b" && u.selectionKey(s) {
			return nil
		}
		if handled, err := u.main.btwKey(s); handled {
			u.selection = nil
			return err
		}
	}

	if s == "\x1b" && u.main != nil && !u.main.keybindings {
		var transcript *activityui.View
		if u.Focus == 0 {
			transcript = u.main.transcriptView()
		}
		if transcript != nil && !transcript.Following {
			transcript.Follow()
			u.selection = nil
			return nil
		}
	}
	if u.selection != nil && !u.selection.Dragging() {
		if s == "\x1b" && u.selectionKey(s) {
			return nil
		}
		// Editing or navigating dismisses the contextual actions.
		u.selection = nil
	}

	if s == "\x1b" && u.Focus == 3 {
		u.Focus = 0
		return nil
	}
	if s == "\x1b" && u.Focus == 0 && u.main != nil {
		if u.main.Turn != "" || u.main.Starting() || u.main.InputPending() || len(u.main.conversation().Unsent)+len(u.main.conversation().Queued) > 0 {
			return u.main.keyboardEscape()
		}
		return nil
	}

	u.Shell.Clamp()
	if u.Focus == 0 {
		quit, err := u.main.input(terminalsession.Input{Kind: terminalsession.KeyInput, Text: s})
		u.main.quitRequested = u.main.quitRequested || quit
		return err
	}
	if u.Focus == 1 {
		if editor, ok := u.Auxiliary.(terminalui.PaneEditor); ok && s == "e" && u.main != nil {
			if path := editor.EditorPath(); path != "" {
				return &openFileEditor{path: path}
			}
		}
		if u.Auxiliary.Key(s) {
			u.DiffOpen, u.Focus = false, 0
		}
		return nil
	}
	only, selected := u.main.sessionPresentation().DisplayingChild(), u.agents.Selected
	u.main.publishActivity()
	quit := u.agents.HandleRosterKey(s)
	u.showRosterPick(only, selected)
	if quit {
		u.Focus = 0
	}
	return nil
}

func (u *terminalUI) mouse(s string) error {
	pointer, ok := terminalsession.ParseMouse(s)
	if !ok {
		return nil
	}
	button, x, y, release := pointer.Button, pointer.X, pointer.Y, pointer.Release
	if u.main != nil {
		u.main.view.SkillsHover = ""
		if button == 35 && !release && u.output == nil && u.Layout.MainSkills.Contains(x, y) {
			u.main.view.SkillsHover = "Main"
		}
	}
	if u.agents != nil && (u.output != nil || !u.Layout.Roster.Contains(x, y)) {
		u.agents.SkillsHover = ""
	}
	if u.output != nil {
		u.outputMouse(button, x, y, release)
		return nil
	}
	if u.selection == nil && !u.Dragging() {
		for pane, rect := range u.Tabs {
			if rect.Contains(x, y) {
				if button == 0 && !release {
					u.prefix = false
					u.selectNativePane(pane)
				}
				return nil
			}
		}
	}
	if u.main != nil && !u.main.composerReadOnly() && u.main.statusPanel != nil && u.Layout.Main.Contains(x, y) {
		if u.selectionMouse(button, x, y, release) {
			return nil
		}
		if !release {
			switch button &^ 28 {
			case 0:
				u.Focus = 0
			case 64:
				u.main.statusPanelKey("\x1b[A")
			case 65:
				u.main.statusPanelKey("\x1b[B")
			}
		}
		return nil
	}
	if u.main != nil && !u.main.composerReadOnly() && u.main.resumePicker != nil && u.Layout.Main.Contains(x, y) {
		if release {
			return nil
		}
		if button&^28 == 0 {
			u.Focus = 0
		}
		return u.main.resumePickerMouse(button&^28, y-u.Layout.Main.Y)
	}
	if u.main != nil && u.main.prompts.Rect.Contains(x-u.Layout.Main.X, y-u.Layout.Main.Y) {
		if !release && button&^28 == 0 {
			u.main.openQuestions()
		}
		return nil
	}
	if u.main != nil && u.main.pickerVisible() && u.main.picker.rect.Contains(x-u.Layout.Main.X, y-u.Layout.Main.Y) {
		if !release {
			switch button &^ 28 {
			case 0:
				p := &u.main.picker
				row := y - u.Layout.Main.Y - p.rect.Y - p.RowStart
				if row >= 0 && row < p.RowCount {
					p.Selected = p.Top + row
					if p.Target.Kind == '/' {
						return u.send("\r")
					}
					u.main.pickerKey("\r")
					u.main.refreshPicker()
				}
			case 64:
				u.main.pickerKey("\x10")
			case 65:
				u.main.pickerKey("\x0e")
			}
		}
		return nil
	}
	if u.selectionMouse(button, x, y, release) {
		return nil
	}
	if u.selection == nil && !u.Dragging() && u.main != nil && u.main.composerNoticeMouse(button, x-u.Layout.Main.X, y-u.Layout.Main.Y, release) {
		return nil
	}
	if u.main != nil && u.main.btw != nil && !release && button&64 != 0 {
		r := u.main.btw.rect
		r.X += u.Layout.Main.X
		r.Y += u.Layout.Main.Y
		if r.Contains(x, y) {
			delta := 3
			if button&1 != 0 {
				delta = -delta
			}
			u.main.btw.scroll = max(0, u.main.btw.scroll+delta)
			return nil
		}
	}
	navigatorWidth := 0
	navigator, hasNavigator := u.Auxiliary.(terminalui.PaneNavigator)
	if hasNavigator {
		navigatorWidth = navigator.NavigatorWidth(u.Layout.Diff.W)
	}
	if handled, columns := u.Panes.Drag(pointer, u.Layout, navigatorWidth); handled {
		if columns != 0 && hasNavigator {
			navigator.SetNavigatorColumns(columns)
		}
		return nil
	}
	if u.Layout.Live.Contains(x, y) {
		if !release {
			switch button &^ 28 {
			case 0:
				u.Focus = 0
			case 64:
				u.liveDock.ScrollBatch(u.main.transcriptCaller(), terminalui.PaneWheelUp)
			case 65:
				u.liveDock.ScrollBatch(u.main.transcriptCaller(), terminalui.PaneWheelDown)
			}
		}
		return nil
	}
	if button&^28 == 0 && !release && u.main != nil && u.Layout.MainSkills.Contains(x, y) {
		u.Focus = 0
		u.openSkills(u.main.view, "Main")
		return nil
	}
	pane := -1
	var r terminalsession.Rect
	switch {
	case u.Layout.Main.Contains(x, y):
		pane = 0
		r = u.Layout.Main
	case u.Layout.Diff.Contains(x, y):
		pane = 1
		r = u.Layout.Diff
	case u.Layout.Roster.Contains(x, y):
		pane = 3
		r = u.Layout.Roster
	}
	if pane < 0 {
		return nil
	}
	if pane != 1 && u.Auxiliary != nil {
		u.Auxiliary.ClearHover()
	}

	if button&^28 == 0 && !release {
		u.Focus = pane
	}
	if pane == 0 {
		if !release {
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
			view := u.main.transcriptView()
			view.HandleMouse(action, y-r.Y+1, x-r.X+1)
			u.openRequested(view)
		}
		return nil
	}
	if release {
		return nil
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
	if pane == 1 && u.Auxiliary != nil {
		u.Auxiliary.Mouse(action, y-r.Y+1, x-r.X+1)
		return nil
	}
	if pane == 3 {
		if action == 'j' {
			u.agents.ScrollRoster(1)
		} else if action == 'k' {
			u.agents.ScrollRoster(-1)
		} else {
			only, selected := u.main.sessionPresentation().DisplayingChild(), u.agents.Selected
			u.agents.PointAgent(action, y-r.Y+1, x-r.X+1)
			if agent := u.agents.SkillsRequest; agent != "" {
				u.agents.SkillsRequest = ""
				u.openSkills(u.agents, agent)
			}
			u.showRosterPick(only, selected)
		}
	} else {
		u.agents.HandleMouse(action, y-r.Y+1, x-r.X+1)
		u.openRequested(u.agents)
	}
	return nil
}

func (u *terminalUI) clearSkillsHover() {
	if u.main != nil {
		u.main.view.SkillsHover = ""
	}
	if u.agents != nil {
		u.agents.SkillsHover = ""
	}
}
