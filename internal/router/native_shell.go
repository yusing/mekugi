package router

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// Live edit docks sit at the bottom of the pane that owns the writer: Main's
// edits above its composer, subagents' at the bottom of the right column.
const (
	nativeDockShare   = 0.3
	nativeDockMin     = 5
	nativeDockMax     = 14
	nativeDockLinger  = 4 * time.Second // A finished dock stays readable this long.
	nativeRosterRows  = 4               // Unfocused roster rows.
	nativeRosterShare = 0.4             // Focused roster share of the screen.
	nativeFramedRows  = 8               // Below this, panes drop their frames.
)

// preview routes one edit card to its caller's dock.
func (u *terminalUI) preview(preview liveDiffPreview) {
	dock, seen := &u.agentDock, &u.dockSeen[1]
	if preview.Caller == "/root" || preview.Caller == "" {
		dock, seen = &u.mainDock, &u.dockSeen[0]
	}
	dock.update(preview)
	*seen = time.Now()
}

// applyNativeDiff sends saved changes to the diff pane and live edit cards to
// the docks. App-server streams apply_patch itself, so the router's
// prediction of the same call is dropped; exec and Code Mode edits remain.
func (u *terminalUI) applyNativeDiff(ctx context.Context, event liveDiffEvent) {
	switch event.Kind {
	case "preview":
		preview := event.Preview
		if preview == nil || preview.Tool == applyPatchToolName ||
			preview.Workspace != "" && !u.diff.scope.Workspaces[preview.Workspace][preview.Thread] {
			return
		}
		u.preview(*preview)
		return
	case "coverage":
		if strings.HasPrefix(event.Status, "RECONNECTING:") {
			u.mainDock, u.agentDock = liveDiffPreviewPane{}, liveDiffPreviewPane{}
		}
	case "change":
		u.diffUnseen = u.diffUnseen || !u.diffOpen
	}
	if u.diffFailure != "" {
		return
	}
	if _, err := u.diff.applyEvent(ctx, event); err != nil {
		u.diffFailure = err.Error()
	}
}

// animating reports whether a dock needs another frame: a call is still
// arriving, a row is fading in, or a finished dock is due to close.
func (u *terminalUI) animating(now time.Time) bool {
	for i, dock := range []*liveDiffPreviewPane{&u.mainDock, &u.agentDock} {
		if len(dock.order) == 0 {
			continue
		}
		if dock.live() > 0 || dock.animating(now) {
			return true
		}
		if now.Sub(u.dockSeen[i]) >= nativeDockLinger {
			*dock = liveDiffPreviewPane{}
			return true
		}
	}
	return false
}

// nextLive opens the next card in the focused pane's dock, or the other dock
// when that one has nothing to cycle.
func (u *terminalUI) nextLive() {
	docks := []*liveDiffPreviewPane{&u.agentDock, &u.mainDock}
	if u.focus == 0 {
		docks[0], docks[1] = docks[1], docks[0]
	}
	for _, dock := range docks {
		if dock.next() {
			return
		}
	}
}

func nativeDockRows(dock *liveDiffPreviewPane, height int) int {
	if len(dock.order) == 0 || height < nativeDockMin+4 {
		return 0
	}
	rows := int(float64(height)*nativeDockShare + .5)
	return min(max(rows, nativeDockMin), nativeDockMax, height-4)
}

// renderDock is the dock's separator row and its cards.
func (u *terminalUI) renderDock(ctx context.Context, dock *liveDiffPreviewPane, width, rows int, focused bool) ([]string, error) {
	theme := u.diff.theme
	dock.motion.enabled = true
	dock.motion.canvas = theme.Canvas()
	if u.diff.backgrounded {
		dock.motion.canvas.Background = u.diff.background
	}
	workspace := u.diff.workspace
	if u.main != nil {
		workspace = cmp.Or(workspace, u.main.session.cwd)
	}
	cards, err := dock.render(ctx, workspace, theme, width-2, rows-1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, caller := range dock.callers() {
		names = append(names, u.agents.painter.agent(caller))
	}
	left := "LIVE · " + strings.Join(names, liveActivityDim+", "+liveActivityUndim)
	if len(names) > 2 {
		left = fmt.Sprintf("LIVE · %d agents", len(names))
	}
	right := ""
	if len(dock.order) > 1 && dock.accordion {
		right = liveActivityDim + "^B e next" + liveActivityUndim
	}
	return append([]string{nativeRule("╞", "╡", "═", left, right, width, nativeBorder(focused))}, cards...), nil
}

func nativeBorder(focused bool) string {
	if focused {
		return "\x1b[38;2;130;170;255m"
	}
	return "\x1b[38;2;70;70;90m"
}

// nativeRule is a pane edge with embedded labels. The right label yields
// first when both do not fit.
func nativeRule(open, close, fill, left, right string, width int, color string) string {
	if width < 2 {
		return ""
	}
	inner := width - 2
	segment := func(label string) string {
		if label == "" {
			return ""
		}
		return " " + label + liveActivityReset + color + " "
	}
	l, r := segment(left), segment(right)
	if ansi.StringWidth(l)+ansi.StringWidth(r)+1 > inner {
		r = ""
	}
	if ansi.StringWidth(l)+1 > inner {
		l = ansi.Truncate(l, max(0, inner-2), "…") + liveActivityReset + color + " "
	}
	gap := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r))
	return color + open + l + strings.Repeat(fill, gap) + r + close + liveActivityReset
}

// nativeBox frames a pane. rules replace whole rows, borders included, so a
// dock separator can span the frame.
func nativeBox(width, height int, title, right string, focused bool, body []string, rules map[int]string) []string {
	if width < 4 || height < 3 {
		return nil
	}
	color := nativeBorder(focused)
	lines := []string{nativeRule("┌", "┐", "─", title, right, width, color)}
	for row := range height - 2 {
		if rule, ok := rules[row]; ok {
			lines = append(lines, rule)
			continue
		}
		text := ""
		if row < len(body) {
			text = ansi.Truncate(body[row], width-2, "")
		}
		lines = append(lines, color+"│"+liveActivityReset+text+strings.Repeat(" ", max(0, width-2-ansi.StringWidth(text)))+color+"│"+liveActivityReset)
	}
	return append(lines, color+"└"+strings.Repeat("─", width-2)+"┘"+liveActivityReset)
}

// nativeTitle labels a pane with its Ctrl-B digit.
func nativeTitle(digit int, name, detail string, focused bool) string {
	label := fmt.Sprintf("%d %s", digit, name)
	if focused {
		label = "\x1b[1m" + label + "\x1b[22m"
	} else {
		label = liveActivityDim + label + liveActivityUndim
	}
	if detail != "" {
		label += liveActivityDim + " · " + liveActivityUndim + detail
	}
	return label
}

// paintNative lays out the app-server shell: Main on the left, the saved
// diff or Activity on the right, each with its live dock, the fitted roster
// below, and a status bar for the focused pane.
func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	width, height := max(1, u.width), max(1, u.height)
	now := time.Now()
	u.agents.feedOnly, u.agents.focused = true, u.focus == 2
	u.agentDock.prefer = ""
	if u.agents.only {
		u.agentDock.prefer = u.agents.selected
	}
	roster := u.agents.nativeRoster(width, u.rosterLimit(height), now, u.focus == 3)
	if height-1-len(roster) < nativeFramedRows {
		roster = nil // A short terminal keeps its rows for Main.
	}
	top := max(0, height-1-len(roster))
	framed := top >= nativeFramedRows
	l := terminalLayout{vertical: -1, horizontal: -1, rosterHorizontal: -1}
	if len(roster) > 0 {
		l.rosterHorizontal = top
		l.roster = terminalRect{0, top, width, len(roster)}
	}
	var left, right terminalRect
	wide := width >= 100 && top >= 12
	switch {
	case wide:
		split := u.split
		if split == 0 {
			split = width / 2
		}
		split = min(max(40, split), width-40)
		left, right = terminalRect{0, 0, split, top}, terminalRect{split, 0, width - split, top}
		l.vertical = split
	case u.focus == 0:
		left = terminalRect{0, 0, width, top}
	default:
		right = terminalRect{0, 0, width, top}
	}
	rows := make([]string, height)
	draw := func(r terminalRect, lines []string) {
		for i, line := range lines {
			if y := r.y + i; y >= 0 && y < len(rows) && i < r.h {
				rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", r.x+1, line)
			}
		}
	}
	if left.w >= 4 && left.h >= 1 {
		iw, ih := left.w-2, left.h-2
		if !framed {
			iw, ih = left.w, left.h
		}
		dockRows := nativeDockRows(&u.mainDock, ih-3)
		body, dockAt := u.main.mainFrame(iw, ih, dockRows)
		rules := map[int]string{}
		if dockRows > 0 && dockAt+dockRows <= len(body) {
			dock, err := u.renderDock(ctx, &u.mainDock, left.w, dockRows, u.focus == 0)
			if err != nil {
				return err
			}
			rules[dockAt] = dock[0]
			for i := 1; i < dockRows; i++ {
				body[dockAt+i] = ""
				if i < len(dock) {
					body[dockAt+i] = dock[i]
				}
			}
		}
		if !framed {
			l.codex = left
			draw(left, body)
		} else {
			l.codex = terminalRect{left.x + 1, left.y + 1, iw, ih}
			draw(left, nativeBox(left.w, left.h, nativeTitle(1, "Main", "", u.focus == 0), scrollLabel(u.main.view), u.focus == 0, body, rules))
		}
	}
	if right.w >= 4 && right.h >= 3 && framed {
		iw, ih := right.w-2, right.h-2
		dockRows := nativeDockRows(&u.agentDock, ih)
		content := ih - dockRows
		focused := u.focus == 1 || u.focus == 2
		var body []string
		var title, label string
		if u.diffOpen {
			l.diff = terminalRect{right.x + 1, right.y + 1, iw, content}
			if u.diffScreen.Width() != iw || u.diffScreen.Height() != content {
				u.diffScreen.Resize(iw, max(1, content))
				u.diff.dirty = true
			}
			u.layout = l // The diff controller sizes itself from the layout.
			if u.diffFailure == "" {
				if err := u.diff.renderFrame(ctx); err != nil {
					u.diffFailure = err.Error()
				}
			}
			if u.diffFailure != "" {
				body = []string{"Diff unavailable", livediff.Safe(u.diffFailure, false), "Use mchanges to review captured edits."}
			} else {
				body = strings.Split(u.diffScreen.Render(), "\n")
			}
			summary, state := u.diff.nativeTitle()
			title, label = nativeTitle(2, "Diff", "saved · "+summary, u.focus == 1), state
			u.diffUnseen = false
		} else {
			l.agents = terminalRect{right.x + 1, right.y + 1, iw, content}
			body = u.agents.render(iw, content, now)
			detail, state := u.agents.nativeTitle()
			title, label = nativeTitle(3, "Activity", detail, u.focus == 2), state
		}
		body = append(body[:min(len(body), content)], make([]string, max(0, content-len(body)))...)
		rules := map[int]string{}
		if dockRows > 0 {
			dock, err := u.renderDock(ctx, &u.agentDock, right.w, dockRows, focused)
			if err != nil {
				return err
			}
			rules[content] = dock[0]
			for i := 1; i < dockRows; i++ {
				line := ""
				if i < len(dock) {
					line = dock[i]
				}
				body = append(body, line)
			}
			body = append(body[:content], append([]string{""}, body[content:]...)...)
		}
		draw(right, nativeBox(right.w, right.h, title, label, focused, body, rules))
	}
	if len(roster) > 0 {
		draw(l.roster, roster)
	}
	rows[height-1] += "\x1b[1G\x1b[0m" + ansi.Truncate(u.nativeStatus(), width, "")
	u.layout = l
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l\x1b[0m")
	for row, line := range rows {
		if u.paintedWidth == u.width && len(u.paintedRows) == len(rows) && line == u.paintedRows[row] {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", row+1, line)
	}
	b.WriteString("\x1b[?2026l")
	_, err := io.WriteString(out, b.String())
	if err == nil {
		u.paintedRows, u.paintedWidth = rows, u.width
	}
	return err
}

// rosterLimit fits the roster to its rows, within a few rows unless focused.
func (u *terminalUI) rosterLimit(height int) int {
	if u.focus == 3 {
		return max(nativeRosterRows, int(float64(height)*nativeRosterShare))
	}
	return nativeRosterRows
}

// nativeStatus shows the pane tabs, with Diff and Activity marked as the pair
// sharing the right column, and only the focused pane's keys.
func (u *terminalUI) nativeStatus() string {
	tab := func(digit int, name, badge string, shown bool) string {
		text := fmt.Sprintf(" %d %s", digit, name)
		switch {
		case u.focus == digit-1:
			text = "\x1b[7;1m" + text + badge + " \x1b[27;22m"
		case shown:
			text += badge + " "
		default:
			text = liveActivityDim + text + liveActivityUndim + badge + " "
		}
		return text
	}
	diffBadge := ""
	if u.diffUnseen {
		diffBadge = liveActivityAmber + "●" + liveActivityReset
	}
	responding, _ := u.agents.statusCounts(u.agents.roster())
	agentsBadge := ""
	if responding > 0 {
		agentsBadge = liveActivityAmber + superscript(responding) + liveActivityReset
	}
	pair := liveActivityDim + "[" + liveActivityUndim + tab(2, "Diff", diffBadge, u.diffOpen) + liveActivityDim + "│" + liveActivityUndim +
		tab(3, "Activity", "", !u.diffOpen) + liveActivityDim + "]" + liveActivityUndim
	tabs := tab(1, "Main", "", true) + " " + pair + " " + tab(4, "Agents", agentsBadge, true)
	var hints string
	switch {
	case u.prefix:
		return tabs + "  " + liveActivityAmber + "Ctrl-B" + liveActivityReset + " 1-4 focus · 2/3 diff or activity · e next live · ←→ resize · PgUp/PgDn history"
	case u.focus == 1:
		hints = "s files · Tab changes · [ ] hunks · a caller · r follow · ? help"
		if u.diff.back.kind != 0 {
			hints = "Esc back · " + hints
		}
	case u.focus == 2:
		hints = "j/k scroll · n/p agent · o only · r follow · Enter open"
	case u.focus == 3:
		hints = "j/k agent · o only · Esc back"
	default:
		hints = "⏎ send · PgUp/PgDn scroll · /quit exits"
	}
	if u.mainDock.live()+u.agentDock.live() > 1 {
		hints += " · ^B e next live"
	}
	return tabs + "  " + liveActivityDim + hints + " · ^B 1-4 panes" + liveActivityUndim
}

func superscript(n int) string {
	digits := []rune("⁰¹²³⁴⁵⁶⁷⁸⁹")
	var b strings.Builder
	for _, r := range fmt.Sprint(n) {
		b.WriteRune(digits[r-'0'])
	}
	return b.String()
}

// showRosterPick keeps a roster pick visible: when it changes Activity's
// filter, the native right pane leaves the saved diff for Activity.
func (u *terminalUI) showRosterPick(only bool, selected string) {
	if u.main != nil && (u.agents.only != only || u.agents.selected != selected) {
		u.diffOpen = false
	}
}

// flushEscape sends a lone Esc once no sequence followed it.
func (u *terminalUI) flushEscape() error {
	if u.sequence != "\x1b" || time.Since(u.sequenceAt) < 40*time.Millisecond {
		return nil
	}
	u.sequence = ""
	return u.send("\x1b")
}
