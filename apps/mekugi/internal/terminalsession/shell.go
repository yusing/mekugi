package terminalsession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	pathterminal "github.com/yusing/mekugi/libraries/pathdisplay/terminal"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Shell owns outer layout and stat-line targets around prepared feature content.
type Shell struct {
	Panes
	Auxiliary      terminalui.Pane
	AuxiliaryLabel string
	RosterLabel    string
	Theme          diffrender.Theme
	Layout         Layout
	Targets        []StatTarget
	StatHover      byte // Action of the stat-line target under the pointer.
}

// StatTarget is a clickable stat-line segment.
type StatTarget struct {
	Rect
	Action byte
}

// Stat-line actions.
const (
	StatRoster byte = 'r'
	StatDiff   byte = 'd'
	StatMain   byte = 'm'
	StatSkills byte = 's'
	StatSide   byte = 'b'
)

type ShellPlan struct {
	Layout
	Left, Right          Rect
	Width, Height, Stats int
	DiffFramed           bool
}

type ShellContent struct {
	Main, Diff, Stats      []string
	DiffSummary, DiffState string
	Dock                   Rect
}

// ShellStatus carries the second stat line's facts: the displayed thread,
// subagent counts, title and the navigation available to an empty composer.
type ShellStatus struct {
	Agent, Title, Scroll, Skills, SideLabel string
	Workspace                               string
	Summary                                 activityui.RosterSummary
	SideSelected, Locked, DiffUnseen        bool
	Prefix, Empty                           bool
	LiveCount                               int
	Playback                                bool
	PlaybackBar                             string
}

// statRows keeps both stat lines unless Main would fall below eight rows.
func (s *Shell) statRows(height int) int {
	switch {
	case height >= 10:
		return 2
	case height >= 3:
		return 1
	}
	return 0
}

func (s *Shell) Plan(width, height int) ShellPlan {
	s.Clamp()
	width, height = max(1, width), max(1, height)
	stats := s.statRows(height)
	layout, left, right, diffFramed := s.Panes.Geometry(width, height, stats)
	return ShellPlan{layout, left, right, width, height, stats, diffFramed}
}

// Compose performs no feature lookup or effects; the caller supplies prepared rows.
// Source: apps/mekugi/internal/router/native_shell.go:292:389@921795f4 paintNative
func (s *Shell) Compose(plan ShellPlan, in ShellContent) []string {
	layout := plan.Layout
	rows := make([]string, plan.Height)
	draw := func(rect Rect, lines []string) {
		prefix := fmt.Sprintf("\x1b[%dG\x1b[0m", rect.X+1)
		for i, line := range lines {
			if y := rect.Y + i; y >= 0 && y < len(rows) && i < rect.H {
				rows[y] += prefix + line + "\x1b[0m"
			}
		}
	}
	if left := plan.Left; left.W >= 4 && left.H >= 1 {
		if in.Dock.H > 0 {
			layout.Live = Rect{layout.Main.X, layout.Main.Y + in.Dock.Y, layout.Main.W, in.Dock.H}
		}
		draw(left, in.Main)
	}
	if right := plan.Right; right.W >= 4 && right.H >= 1 {
		body := append(in.Diff[:min(len(in.Diff), layout.Diff.H)], make([]string, max(0, layout.Diff.H-len(in.Diff)))...)
		if plan.DiffFramed {
			title := terminalui.PaneTitle(2, s.AuxiliaryLabel, in.DiffSummary, s.Focus == 1)
			draw(right, terminalui.PaneBox(right.W, right.H, title, in.DiffState, s.Focus == 1, body, nil))
		} else {
			draw(right, body)
		}
	}
	stats := in.Stats[max(0, len(in.Stats)-plan.Stats):]
	for i, line := range stats {
		rows[plan.Height-len(stats)+i] += "\x1b[1G\x1b[0m" + line
	}
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

// statSegment is one stat-line part. Segments with a higher drop rank leave
// first when the line is too narrow; rank zero is kept.
type statSegment struct {
	text   string
	action byte
	drop   int
	path   string // A workspace shortened before any segment drops.
}

// StatLine paints the second stat line at row y:
// agent • counts • title • workspace • ↓ roster • ← main | → diff • ^E view.
// Navigation hints show only while their keys act, with an empty composer.
// A clickable segment underlines while the pointer is over it.
// Narrow lines abbreviate the workspace to a sixth of the line and shorten
// the title, then drop pane keys, ^E view, live batches, net lines, skills,
// the workspace, the side thread, finished counts, the scroll position, Diff
// and roster hints, and finally the remaining counts.
func (s *Shell) StatLine(in ShellStatus, width, y int) string {
	s.Clamp()
	s.Targets = s.Targets[:0]
	if in.Playback {
		return in.PlaybackBar
	}
	const sep = terminalui.Dim + " • " + terminalui.Undim
	hint := func(key, label string) string { return key + terminalui.Dim + " " + label + terminalui.Undim }
	agent := statSegment{text: "\x1b[1m" + s.Theme.Accent() + in.Agent + terminalui.Reset}
	var segments []statSegment
	add := func(text string, action byte, drop int) {
		if text != "" {
			segments = append(segments, statSegment{text: text, action: action, drop: drop})
		}
	}
	if in.SideLabel != "" && !in.SideSelected {
		add(terminalui.Dim+in.SideLabel+terminalui.Undim, StatSide, 7)
	}
	for _, count := range in.Summary.Counts {
		drop := 2
		if strings.Contains(count, "✓") {
			drop = 6
		}
		add(count, 0, drop)
	}
	title := len(segments)
	if cwd := strings.ReplaceAll(textsanitize.Safe(in.Workspace, false), "\n", " "); cwd != "" {
		segments = append(segments, statSegment{text: statWorkspace(cwd), drop: 8, path: cwd})
	}
	add(in.Scroll, 0, 5)
	if in.Skills != "" {
		add(terminalui.Dim+in.Skills+terminalui.Undim, StatSkills, 9)
	}
	if in.Locked {
		add(terminalui.Amber+"Locked · /unlock"+terminalui.Reset, 0, 0)
	}
	if in.Prefix {
		selector := "1"
		switch {
		case s.Auxiliary != nil && s.RosterLabel != "":
			selector = "1-3"
		case s.Auxiliary != nil:
			selector = "1-2"
		case s.RosterLabel != "":
			selector = "1,3"
		}
		hints := terminalui.Hints{{"ctrl+b " + selector, "select", 0}}
		if s.Auxiliary != nil {
			hints = append(hints, terminalui.Hint{"2", "toggle " + strings.ToLower(s.AuxiliaryLabel), 0})
		}
		hints = append(hints, terminalui.Hints{{"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}...)
		add(hints.Render(), 0, 1)
	} else {
		// The roster sheet shows its own keys.
		if in.Empty && in.Summary.Children && s.RosterLabel != "" && s.Focus == 0 {
			add(hint("↓", "roster"), StatRoster, 3)
		}
		if in.Empty && s.Auxiliary != nil && s.Focus != 3 {
			badge := ""
			if in.DiffUnseen {
				badge = " " + terminalui.Amber + "●" + "\x1b[39m"
			}
			if s.Focus == 1 {
				add(hint("←", "main")+badge, StatMain, 4)
			} else {
				add(hint("→", strings.ToLower(s.AuxiliaryLabel))+badge, StatDiff, 4)
			}
		}
		add(in.Summary.Net, 0, 10)
		if in.LiveCount > 1 {
			add(hint("^B e", "next live"), 0, 11)
		}
		switch s.Focus {
		case 0:
			add(hint("^E", "view"), 0, 12)
		case 1:
			add(s.Auxiliary.Hints().Render(), 0, 13)
		}
	}
	// Aligned with the caption above.
	const indent = "  "
	room := max(1, width-1-len(indent))
	measure := func() int {
		used := ansi.StringWidth(agent.text)
		for _, segment := range segments {
			used += 3 + ansi.StringWidth(segment.text)
		}
		return used
	}
	// The title takes what the other segments leave, down to eight cells.
	const titleFloor = 8
	titleText := ""
	if in.Title != "" {
		titleText = in.Title
	}
	for {
		used := measure()
		if titleText != "" {
			used += 3 + min(titleFloor, ansi.StringWidth(titleText))
		}
		if used <= room {
			break
		}
		if shortenWorkspace(segments, max(width/6, 8), used-room) {
			continue
		}
		worst := -1
		for i, segment := range segments {
			if segment.drop > 0 && (worst < 0 || segment.drop > segments[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			if titleText != "" {
				titleText = ""
				continue
			}
			break
		}
		if worst < title {
			title--
		}
		segments = slices.Delete(segments, worst, worst+1)
	}
	// Later drops can leave room for more of an abbreviated workspace.
	if titleText == "" {
		for i, segment := range segments {
			if segment.path != "" {
				segments[i].text = statWorkspace(pathterminal.Truncate(segment.path, ansi.StringWidth(segment.text)+room-measure()))
			}
		}
	}
	if titleText != "" {
		// The title takes room from the workspace before it truncates.
		if over := ansi.StringWidth(titleText) - (room - measure() - 3); over > 0 {
			shortenWorkspace(segments, max(width/6, 8), over)
		}
		fit := max(1, room-measure()-3)
		if ansi.StringWidth(titleText) > fit {
			titleText = strings.TrimRight(ansi.Truncate(titleText, fit-1, ""), " ") + "…"
		}
		segments = slices.Insert(segments, title, statSegment{text: titleText})
	}
	var line strings.Builder
	line.WriteString(agent.text)
	for _, segment := range segments {
		line.WriteString(sep)
		x := ansi.StringWidth(line.String())
		if segment.action != 0 && segment.action == s.StatHover {
			line.WriteString("\x1b[4m" + segment.text + "\x1b[24m")
		} else {
			line.WriteString(segment.text)
		}
		if segment.action != 0 && x < room {
			s.Targets = append(s.Targets, StatTarget{Rect{len(indent) + x, y, min(ansi.StringWidth(segment.text), room-x), 1}, segment.action})
		}
	}
	return indent + ansi.Truncate(line.String(), room, "…") + terminalui.Reset
}
func statWorkspace(path string) string { return captionWorkspace + path + terminalui.Reset }

// shortenWorkspace abbreviates the workspace segment by over cells, down to
// floor, and reports whether it shortened.
func shortenWorkspace(segments []statSegment, floor, over int) bool {
	for i, segment := range segments {
		if segment.path == "" {
			continue
		}
		width := ansi.StringWidth(segment.text)
		if width <= floor {
			return false
		}
		segments[i].text = statWorkspace(pathterminal.Truncate(segment.path, max(floor, width-over)))
		return true
	}
	return false
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
