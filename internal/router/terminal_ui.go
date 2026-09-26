package router

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

// The native shell owns terminal presentation; Codex app-server owns execution.
type terminalUI struct {
	pasteEnd                                       int
	hostReply                                      []byte
	hostReplyDiscard, hostReplyEscape              bool
	diffFailure                                    string
	diffScreen                                     *vt.Emulator
	diff                                           *liveDiffTerminalController
	agents                                         *liveActivityView
	main                                           *appServerUI
	auto                                           *autoLiveDiff
	width, height, split, horizontal, rosterHeight int
	focus, drag                                    int // 0 Main, 1 diff, 2 agents, 3 roster; drag 1 main, 2 auxiliary, 3 files, 4 roster
	side, activityOpen                             bool
	diffOpen                                       bool
	diffUnseen                                     bool // Saved changes arrived while Activity held the right column.
	mainDock, agentDock                            liveDiffPreviewPane
	dockSeen                                       [2]time.Time // Last card update in each dock.
	prefix                                         bool
	sequenceAt                                     time.Time
	sequence, agentEscape                          string
	paste                                          bool
	layout                                         terminalLayout
	paintedRows                                    []string
	paintedWidth                                   int
}

type terminalRect struct{ x, y, w, h int }
type terminalLayout struct {
	codex, diff, agents, roster            terminalRect
	vertical, horizontal, rosterHorizontal int
}

func (r terminalRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// Decode once before routing. In particular, mouse reports and bracketed paste
// must never leak their payload into pane commands or layout shortcuts.
func (u *terminalUI) key(key byte) error {
	// Paste payload is opaque, including incomplete OSC/CSI and shortcut bytes.
	// Forward it immediately; only the exact end marker changes parser state.
	if u.paste {
		const end = "\x1b[201~"
		if key == end[u.pasteEnd] {
			u.pasteEnd++
		} else if key == 27 {
			u.pasteEnd = 1
		} else {
			u.pasteEnd = 0
		}
		if u.pasteEnd == len(end) {
			u.paste = false
			u.pasteEnd = 0
		}
		if u.focus == 0 {
			return u.send(string([]byte{key}))
		}
		return nil
	}

	// Terminal OSC replies are never pane input; color reports style the panes.
	if u.hostReply != nil {
		done := key == 7 || u.hostReplyEscape && key == '\\'
		u.hostReplyEscape = key == 27
		if len(u.hostReply) < 4<<20 && !u.hostReplyDiscard {
			u.hostReply = append(u.hostReply, key)
		} else {
			u.hostReplyDiscard = true
		}
		if !done {
			return nil
		}
		if !u.hostReplyDiscard {
			u.terminalColor(string(u.hostReply))
		}
		u.hostReply = nil
		u.hostReplyDiscard = false
		u.hostReplyEscape = false
		return nil
	}
	if u.sequence == "\x1b" && key == ']' {
		u.sequence = ""
		u.hostReply = []byte{27, ']'}
		return nil
	}

	if u.sequence != "" {
		u.sequence += string(key)
		if len(u.sequence) > 128 {
			u.sequence = ""
			return nil
		}
		s := u.sequence
		if s == "\x1b[" || s == "\x1bO" {
			return nil
		}
		if len(s) > 2 && s[1] == '[' && (key < 0x40 || key > 0x7e) {
			return nil
		}
		u.sequence = ""
		if s == "\x1b[200~" {
			u.paste = true
			u.pasteEnd = 0
			u.prefix = false
			if u.focus == 0 {
				return u.send(s)
			}
			return nil
		}
		if s == "\x1b[201~" {
			u.paste = false
			if u.focus == 0 {
				return u.send(s)
			}
			return nil
		}
		if strings.HasPrefix(s, "\x1b[<") {
			if u.main != nil && u.main.keybindings {
				u.main.keybindings = false
				return nil
			}
			return u.mouse(s)
		}
		if u.prefix {
			u.prefix = false
			u.resize(s)
			return nil
		}
		return u.send(s)
	}
	if key == 27 {
		u.sequence = "\x1b"
		u.sequenceAt = time.Now()
		return nil
	}
	if u.prefix {
		u.prefix = false
		switch key {
		case 2:
			return u.send(string([]byte{2}))
		case '1':
			u.focus = 0
		case '2':
			u.focus = 1
			u.side = true
			u.diffOpen = true
		case '3', '4':
			u.focus = int(key - '1')
			u.side = true
			u.activityOpen = true
			// Native Diff and Activity share the right column.
			u.diffOpen = u.diffOpen && key == '4'
		case 'e':
			u.nextLive()
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
	return u.send(string([]byte{key}))
}

func (u *terminalUI) resize(key string) {
	if key == "\x1b[5~" || key == "\x1b[6~" {
		if key == "\x1b[5~" {
			u.main.view.scrollKey('b')
		} else {
			u.main.view.scrollKey(' ')
		}
		return
	}
	if u.split == 0 {
		u.split = max(30, u.width/2)
	}
	if u.horizontal == 0 {
		u.horizontal = max(4, (u.height-1)*3/5)
		if u.layout.horizontal > 0 {
			u.horizontal = u.layout.horizontal
		}
	}
	// The bottom roster leaves the columns above it at least nine rows.
	columns := u.height - 1
	if u.layout.rosterHorizontal >= 0 {
		columns = u.layout.rosterHorizontal
	}
	if u.focus == 3 && (key == "\x1b[A" || key == "k" || key == "\x1b[B" || key == "j") {
		if u.rosterHeight == 0 {
			u.rosterHeight = max(3, (u.height-1)/5)
		}
		if key == "\x1b[A" || key == "k" {
			u.rosterHeight++
		} else {
			u.rosterHeight--
		}
		u.rosterHeight = min(max(3, u.rosterHeight), max(3, u.height-11))
		return
	}
	switch key {
	case "\x1b[D", "h":
		u.split -= 3
	case "\x1b[C", "l":
		u.split += 3
	case "\x1b[A", "k":
		u.horizontal--
	case "\x1b[B", "j":
		u.horizontal++
	case "[", "]":
		n := &u.diff.navigation
		if n.columns == 0 {
			n.columns = max(25, u.layout.diff.w/4)
		}
		if key == "[" {
			n.columns -= 2
		} else {
			n.columns += 2
		}
		n.columns = max(16, n.columns)
		u.diff.dirty = true
	}
	u.split = min(max(30, u.split), max(30, u.width-41))
	u.horizontal = min(max(4, u.horizontal), max(4, columns-5))
}

// terminalColor applies OSC 10/11 default-color reports to every pane, as the
// standalone Activity and diff views did for their own queries.
func (u *terminalUI) terminalColor(reply string) {
	views := []*liveActivityView{u.agents}
	if u.main != nil {
		views = append(views, u.main.view)
	}
	if fg, ok := livediff.ForegroundColor(reply); ok {
		for _, view := range views {
			if view != nil {
				view.painter.colors.foreground, view.painter.colors.hasForeground = fg, true
			}
		}
	}
	theme, ok := livediff.BackgroundTheme(reply)
	if !ok {
		return
	}
	bg, _ := livediff.BackgroundColor(reply)
	for _, view := range views {
		if view != nil {
			view.painter.theme = theme
			view.painter.colors.background, view.painter.colors.hasBackground = bg, true
		}
	}
	if u.diff != nil {
		u.diff.theme, u.diff.background, u.diff.backgrounded = theme, bg, true
		u.diff.dirty = true
	}
}

func (u *terminalUI) send(s string) error {
	if u.focus == 0 {
		for _, key := range []byte(s) {
			quit, err := u.main.key(key)
			if err != nil {
				return err
			}
			u.main.quitRequested = u.main.quitRequested || quit
		}
		return nil
	}
	for _, key := range []byte(s) {
		if u.focus == 1 {
			if u.diff.handleKey(key) {
				u.focus = 0
			}
		} else {
			var quit bool
			if u.focus == 3 {
				only, selected := u.agents.only, u.agents.selected
				u.agentEscape, quit = u.agents.handleRosterKey(u.agentEscape, key)
				u.showRosterPick(only, selected)
			} else {
				u.agentEscape, quit = u.agents.handleKey(u.agentEscape, key)
			}
			if quit {
				u.focus = 0
			}
		}
	}
	return nil
}

func (u *terminalUI) mouse(s string) error {
	fields := strings.Split(s[3:len(s)-1], ";")
	if len(fields) != 3 {
		return nil
	}
	var v [3]int
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil
		}
		v[i] = n
	}
	button, x, y := v[0], v[1]-1, v[2]-1
	release := s[len(s)-1] == 'm'
	if release && u.drag != 0 {
		u.drag = 0
		return nil
	}
	if button&32 != 0 && u.drag != 0 {
		switch u.drag {
		case 1:
			u.split = x
		case 2:
			u.horizontal = y
		case 4:
			u.rosterHeight = min(max(3, u.height-y-2), max(3, u.height-11))
		case 3:
			u.diff.navigation.columns = max(16, x-u.layout.diff.x)
			u.diff.dirty = true
		}
		return nil
	}
	if button&^28 == 0 && !release {
		switch {
		case x == u.layout.vertical && u.layout.vertical >= 0 && (u.layout.rosterHorizontal < 0 || y < u.layout.rosterHorizontal):
			u.drag = 1
			return nil
		case y == u.layout.rosterHorizontal && u.layout.rosterHorizontal >= 0:
			u.drag = 4
			return nil
		case y == u.layout.horizontal && x > u.layout.vertical && u.layout.horizontal >= 0:
			u.drag = 2
			return nil
		case u.layout.diff.contains(x, y) && u.diff.diffMode && u.diff.navigation.width(u.layout.diff.w) > 0 && x-u.layout.diff.x == u.diff.navigation.width(u.layout.diff.w):
			u.drag = 3
			return nil
		}
	}
	pane := -1
	var r terminalRect
	switch {
	case u.layout.codex.contains(x, y):
		pane = 0
		r = u.layout.codex
	case u.layout.diff.contains(x, y):
		pane = 1
		r = u.layout.diff
	case u.layout.roster.contains(x, y):
		pane = 3
		r = u.layout.roster
	case u.layout.agents.contains(x, y):
		pane = 2
		r = u.layout.agents
	}
	if pane < 0 {
		return nil
	}
	if pane != 1 && u.diff != nil && u.diff.clearHover() {
		u.diff.dirty = true
	}
	if button&^28 == 0 && !release {
		u.focus = pane
	}
	translated := fmt.Sprintf("\x1b[<%d;%d;%dM", button, x-r.x+1, y-r.y+1)
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
			u.main.view.handleMouse(action, y-r.y+1, x-r.x+1)
		}
		return nil
	}
	if release {
		return nil
	}
	if pane == 1 {
		for _, key := range []byte(translated) {
			u.diff.handleKey(key)
		}
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
	if pane == 3 {
		if action == 'j' {
			u.agents.scrollRoster(1)
		} else if action == 'k' {
			u.agents.scrollRoster(-1)
		} else {
			only, selected := u.agents.only, u.agents.selected
			u.agents.pointAgent(action, y-r.y+1, x-r.x+1)
			u.showRosterPick(only, selected)
		}
	} else {
		u.agents.handleMouse(action, y-r.y+1, x-r.x+1)
	}
	return nil
}

func (u *terminalUI) applyDiff(ctx context.Context, event liveDiffEvent) {
	u.applyNativeDiff(ctx, event)
}
