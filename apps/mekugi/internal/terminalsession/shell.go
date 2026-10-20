package terminalsession

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Shell owns outer layout and status targets around prepared feature content.
type Shell struct {
	Panes
	Auxiliary      terminalui.Pane
	AuxiliaryLabel string
	RosterLabel    string
	Theme          diffrender.Theme
	Layout         Layout
	Tabs           [4]Rect
}

type ShellPlan struct {
	Layout
	Left, Right           Rect
	Width, Height, Roster int
	Framed                bool
}

type ShellContent struct {
	Main, Diff, Roster                 []string
	MainName, MainDetail, MainRight    string
	MainSkills, DiffSummary, DiffState string
	Dock                               Rect
	Status                             ShellStatus
}

type ShellStatus struct {
	Child, Locked, DiffUnseen, Prefix bool
	Responding, LiveCount             int
	Expansion                         uint8
	Playback                          bool
	PlaybackBar                       string
}

func (s *Shell) Plan(width, height, roster int) ShellPlan {
	s.Clamp()
	width, height = max(1, width), max(1, height)
	if s.RosterLabel == "" {
		roster = 0
	}
	layout, left, right, framed, kept := s.Panes.Geometry(width, height, roster)
	return ShellPlan{layout, left, right, width, height, kept, framed}
}

// Compose performs no feature lookup or effects; the caller supplies prepared rows.
// Source: apps/mekugi/internal/router/native_shell.go:292:389@921795f4 paintNative
func (s *Shell) Compose(plan ShellPlan, in ShellContent) []string {
	layout := plan.Layout
	rows := make([]string, plan.Height)
	draw := func(rect Rect, lines []string) {
		for i, line := range lines {
			if y := rect.Y + i; y >= 0 && y < len(rows) && i < rect.H {
				rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", rect.X+1, line)
			}
		}
	}
	if left := plan.Left; left.W >= 4 && left.H >= 1 {
		if in.Dock.H > 0 {
			layout.Live = Rect{layout.Main.X, layout.Main.Y + in.Dock.Y, layout.Main.W, in.Dock.H}
		}
		if !plan.Framed {
			draw(left, in.Main)
		} else {
			title := terminalui.PaneTitle(1, in.MainName, in.MainDetail, s.Focus == 0)
			// A truncated title has no reliable skill target.
			if in.MainSkills != "" && strings.HasSuffix(ansi.Strip(in.MainDetail), in.MainSkills) && ansi.StringWidth(title)+5 <= left.W {
				end := left.X + 2 + ansi.StringWidth(title)
				layout.MainSkills = Rect{end - ansi.StringWidth(in.MainSkills), left.Y, ansi.StringWidth(in.MainSkills), 1}
			}
			draw(left, terminalui.PaneBox(left.W, left.H, title, in.MainRight, s.Focus == 0, in.Main, nil))
		}
	}
	if right := plan.Right; right.W >= 4 && right.H >= 1 {
		body := append(in.Diff[:min(len(in.Diff), layout.Diff.H)], make([]string, max(0, layout.Diff.H-len(in.Diff)))...)
		if plan.Framed {
			title := terminalui.PaneTitle(2, s.AuxiliaryLabel, in.DiffSummary, s.Focus == 1)
			draw(right, terminalui.PaneBox(right.W, right.H, title, in.DiffState, s.Focus == 1, body, nil))
		} else {
			draw(right, body)
		}
	}
	draw(layout.Roster, in.Roster[:min(len(in.Roster), plan.Roster)])
	rows[plan.Height-1] += "\x1b[1G\x1b[0m" + ansi.Truncate(s.Status(in.Status, plan.Width, plan.Height), plan.Width, "")
	s.Layout = layout
	return rows
}

// PromptVisible admits only the part inside Main and outside active overlays.
// Source: apps/mekugi/internal/router/native_shell.go:399:405@921795f4 paintNative
func (s *Shell) PromptVisible(prompt Rect, dialog bool, selection Rect) bool {
	prompt.H = min(prompt.H, max(0, s.Layout.Main.H-prompt.Y))
	prompt.X, prompt.Y = prompt.X+s.Layout.Main.X, prompt.Y+s.Layout.Main.Y
	return prompt.W > 0 && prompt.H > 0 && !dialog &&
		(selection.W <= 0 || selection.H <= 0 || prompt.X+prompt.W <= selection.X || selection.X+selection.W <= prompt.X || prompt.Y+prompt.H <= selection.Y || selection.Y+selection.H <= prompt.Y)
}

// Status shows selectors and only the focused pane's keys.
// Source: apps/mekugi/internal/router/native_shell.go:417:482@921795f4 nativeStatus
func (s *Shell) Status(in ShellStatus, width, height int) string {
	s.Clamp()
	s.Tabs = [4]Rect{}
	if in.Playback {
		return in.PlaybackBar
	}
	var tabs strings.Builder
	tab := func(digit int, name, badge string, shown bool) {
		text := fmt.Sprintf(" %d %s", digit, name)
		switch {
		case (s.Focus == digit-1 && digit != 3 || digit == 3 && s.Focus == 3) && (digit != 1 || !in.Child):
			text = "\x1b[7;1m" + text + badge + " \x1b[27;22m"
		case shown:
			text += badge + " "
		default:
			text = terminalui.Dim + text + terminalui.Undim + badge + " "
		}
		left := ansi.StringWidth(tabs.String())
		pane := digit - 1
		if digit == 3 {
			pane = 3
		}
		s.Tabs[pane] = Rect{left, height - 1, max(0, min(ansi.StringWidth(text), width-left)), 1}
		tabs.WriteString(text)
	}
	diffBadge := ""
	if in.DiffUnseen {
		diffBadge = terminalui.Amber + " ●" + "\x1b[39m"
	}
	agentsBadge := ""
	if in.Responding > 0 {
		agentsBadge = terminalui.Amber + superscript(in.Responding) + "\x1b[39m"
	}
	tab(1, "Main", "", !in.Child)
	if s.Auxiliary != nil {
		tab(2, s.AuxiliaryLabel, diffBadge, s.DiffOpen)
	}
	if s.RosterLabel != "" {
		tab(3, s.RosterLabel, agentsBadge, true)
	}
	if in.Locked {
		tabs.WriteString("  " + terminalui.Amber + "Locked · /unlock" + terminalui.Reset)
	}
	filter := "child"
	if in.Child {
		filter = "Main"
	}
	selector := "1"
	switch {
	case s.Auxiliary != nil && s.RosterLabel != "":
		selector = "1-3"
	case s.Auxiliary != nil:
		selector = "1-2"
	case s.RosterLabel != "":
		selector = "1,3"
	}
	var hints terminalui.Hints
	switch {
	case in.Prefix:
		hints = terminalui.Hints{{"ctrl+b " + selector, "select", 0}}
		if s.Auxiliary != nil {
			hints = append(hints, terminalui.Hint{"2", "toggle " + strings.ToLower(s.AuxiliaryLabel), 0})
		}
		hints = append(hints, terminalui.Hints{{"o", "older child history", 0}, {"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}...)
		return tabs.String() + "  " + hints.Render()
	case s.Focus == 1:
		hints = s.Auxiliary.Hints()
	case s.Focus == 3:
		hints = terminalui.Hints{{"j/k", "agent", 0}, {"a", filter, 0}, {"esc", "back", 0}}
	}
	if in.LiveCount > 1 {
		hints = append(hints, terminalui.Hint{"^B e", "next live", 0})
	}
	hints = append(hints, terminalui.Hint{"^B " + selector, "views", 0})
	if s.Focus == 0 {
		hints = append(hints, terminalui.Hint{"^E", [3]string{"expand events", "expand all", "default"}[in.Expansion], 0})
	}
	return tabs.String() + "  " + hints.Render()
}

func superscript(n int) string {
	digits := []rune("⁰¹²³⁴⁵⁶⁷⁸⁹")
	var b strings.Builder
	for _, r := range fmt.Sprint(n) {
		b.WriteRune(digits[r-'0'])
	}
	return b.String()
}

// Clamp discards focus and geometry for an absent contribution.
func (s *Shell) Clamp() {
	if s.RosterLabel == "" && s.Focus == 3 {
		s.Focus = 0
	}
	if s.Auxiliary == nil {
		s.DiffOpen = false
		if s.Focus == 1 {
			s.Focus = 0
		}
	}
}

func (s *Shell) Select(pane int) {
	if pane == 1 && s.Auxiliary == nil || pane == 3 && s.RosterLabel == "" {
		return
	}
	s.Panes.Select(pane)
}
