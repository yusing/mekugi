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
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// With active subagents, live edits dock in their owning panes. Otherwise
// Live occupies the right column, above the switchable Activity or Diff view.
const (
	nativeDockShare   = 0.3
	nativeDockMin     = 5
	nativeDockMax     = 14
	nativeDockLinger  = 2 * time.Second // A finished dock stays readable this long.
	nativeRosterRows  = 4               // Unfocused roster rows.
	nativeRosterShare = 0.4             // Focused roster share of the screen.
	nativeFramedRows  = 8               // Below this, panes drop their frames.
)

// A Main excerpt carries an Activity identity, not a nearby question or run.
func (u *terminalUI) openActivityReply(seq uint64) bool {
	v := u.main.view
	for _, entry := range v.entries {
		if entry.Seq != seq || entry.activitySeq == 0 {
			continue
		}
		for _, target := range u.agents.entries {
			if target.Seq != entry.activitySeq {
				continue
			}
			u.side, u.activityOpen, u.diffOpen, u.focus = true, true, false, 2
			u.agents.selected, u.agents.only = target.Agent, true
			u.agents.runs = nil
			u.agents.pendingTarget = target.Seq
			return true
		}
	}
	return false
}

// preview routes one edit card to its caller's dock.
func (u *terminalUI) preview(preview diffview.Preview) {
	dock, seen := &u.agentDock, &u.dockSeen[1]
	if preview.Caller == "/root" || preview.Caller == "" {
		dock, seen = &u.mainDock, &u.dockSeen[0]
	}
	// A shell projection first discovered at completion is no longer a live
	// stream. Keep its captured receipt and saved diff, without flashing a dock.
	if preview.Complete && (preview.Tool == nativeExecCommandToolName || preview.Tool == "exec") && dock.Views[preview.ID] == nil {
		return
	}
	dock.Update(preview)
	*seen = time.Now()
}

// applyNativeDiff sends saved changes to the diff pane and live edit cards to
// the docks. All edits use the shared router preview and capture owners.
func (u *terminalUI) applyNativeDiff(ctx context.Context, event liveDiffEvent) {
	switch event.Kind {
	case "preview":
		preview := event.Preview
		if preview == nil ||
			preview.Workspace != "" && !u.diff.scope.Workspaces[preview.Workspace][preview.Thread] {
			return
		}
		u.preview(*preview)
		return
	case "coverage":
		if strings.HasPrefix(event.Status, "RECONNECTING:") {
			u.mainDock, u.agentDock = diffview.PreviewPane{}, diffview.PreviewPane{}
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
	if u.main != nil && (event.Kind == "scope" || event.Kind == "change") {
		u.main.applyCapturedEdits()
	}
}

// animating reports whether a dock needs another frame: a call is still
// arriving, a row is fading in, or a finished dock is due to close.
func (u *terminalUI) animating(now time.Time) bool {
	for i, dock := range []*diffview.PreviewPane{&u.mainDock, &u.agentDock} {
		if len(dock.Order) == 0 {
			continue
		}
		if dock.Live() > 0 || dock.Animating(now) {
			u.dockSeen[i] = now
			return true
		}
		if now.Sub(u.dockSeen[i]) >= nativeDockLinger {
			*dock = diffview.PreviewPane{}
			return true
		}
	}
	return false
}

// nextLive opens the next card in the focused pane's dock, or the other dock
// when that one has nothing to cycle.
func (u *terminalUI) nextLive() {
	docks := []*diffview.PreviewPane{&u.agentDock, &u.mainDock}
	if u.focus == 0 {
		docks[0], docks[1] = docks[1], docks[0]
	}
	for _, dock := range docks {
		if dock.Next() {
			return
		}
	}
}

func nativeDockRows(dock *diffview.PreviewPane, height int) int {
	if len(dock.Order) == 0 || height < nativeDockMin+4 {
		return 0
	}
	rows := int(float64(height)*nativeDockShare + .5)
	return min(max(rows, nativeDockMin), nativeDockMax, height-4)
}

// renderDock is the dock's separator row and its cards.
func (u *terminalUI) renderDock(ctx context.Context, dock *diffview.PreviewPane, width, rows int, focused bool) ([]string, error) {
	theme := u.diff.theme
	dock.Motion.Enabled = true
	dock.Motion.Canvas = theme.Canvas()
	if u.diff.backgrounded {
		dock.Motion.Canvas.Background = u.diff.background
	}
	workspace := u.diff.workspace
	if u.main != nil {
		workspace = cmp.Or(workspace, u.main.session.cwd)
	}
	cards, err := dock.Render(ctx, workspace, theme, width-2, rows-1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, caller := range dock.Callers() {
		names = append(names, u.agents.painter.Agent(caller))
	}
	left := "LIVE · " + strings.Join(names, activityui.Dim+", "+activityui.Undim)
	if len(names) > 2 {
		left = fmt.Sprintf("LIVE · %d agents", len(names))
	}
	right := ""
	if len(dock.Order) > 1 && dock.Accordion {
		right = activityui.Dim + "^B e next" + activityui.Undim
	}
	return append([]string{nativeRule("╞", "╡", "═", left, right, width, nativeBorder(focused))}, cards...), nil
}

// renderIdleLive keeps both owners' cards visible without moving preview state.
func (u *terminalUI) renderIdleLive(ctx context.Context, width, height int, focused bool) ([]string, error) {
	docks := []*diffview.PreviewPane{&u.mainDock, &u.agentDock}
	if len(u.mainDock.Order) == 0 {
		docks = docks[1:]
	} else if len(u.agentDock.Order) == 0 {
		docks = docks[:1]
	}
	var lines []string
	for i, dock := range docks {
		rows := (height - len(lines)) / (len(docks) - i)
		if rows == 0 {
			continue
		}
		card, err := u.renderDock(ctx, dock, width, rows, focused)
		if err != nil {
			return nil, err
		}
		// The dock is inside the pane frame, so its rule uses the inner width.
		card[0] = ansi.Cut(card[0], 1, width-1)
		card = append(card[:min(len(card), rows)], make([]string, max(0, rows-len(card)))...)
		lines = append(lines, card...)
	}
	return lines, nil
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
		return " " + label + activityui.Reset + color + " "
	}
	l, r := segment(left), segment(right)
	if ansi.StringWidth(l)+ansi.StringWidth(r)+1 > inner {
		r = ""
	}
	if ansi.StringWidth(l)+1 > inner {
		l = ansi.Truncate(l, max(0, inner-2), "…") + activityui.Reset + color + " "
	}
	gap := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r))
	return color + open + l + strings.Repeat(fill, gap) + r + close + activityui.Reset
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
		lines = append(lines, color+"│"+activityui.Reset+text+strings.Repeat(" ", max(0, width-2-ansi.StringWidth(text)))+color+"│"+activityui.Reset)
	}
	return append(lines, color+"└"+strings.Repeat("─", width-2)+"┘"+activityui.Reset)
}

// nativeTitle labels a pane with its Ctrl-B digit.
func nativeTitle(digit int, name, detail string, focused bool) string {
	label := fmt.Sprintf("%d %s", digit, name)
	if focused {
		label = "\x1b[1m" + label + "\x1b[22m"
	} else {
		label = activityui.Dim + label + activityui.Undim
	}
	if detail != "" {
		label += activityui.Dim + " · " + activityui.Undim + detail
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
	u.agentDock.Prefer = ""
	if u.agents.only {
		u.agentDock.Prefer = u.agents.selected
	}
	children, active := 0, false
	for _, agent := range u.agents.agents {
		if agent.Name != "/root" {
			children++
			active = active || agent.Responding
		}
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
	liveRight := !active && (len(u.mainDock.Order) > 0 || len(u.agentDock.Order) > 0) && right.w >= 4 && right.h >= 3 && framed
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
		if liveRight || u.main.keybindings && u.focus == 0 {
			dockRows = 0
		}
		body, dockRect := u.main.mainFrame(iw, ih, dockRows)
		dockAt, dockRows := dockRect.y, dockRect.h
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
		topDock := liveRight
		if topDock {
			dockRows = max(1, int(float64(ih)*.35+.5))
			if children == 0 && !u.diffOpen {
				dockRows = ih
			}
		}
		content := ih - dockRows
		contentY := right.y + 1
		if topDock {
			contentY += dockRows
			l.live = terminalRect{right.x + 1, right.y + 1, iw, dockRows}
		}
		focused := u.focus == 1 || u.focus == 2
		var body []string
		var title, label string
		if u.diffOpen {
			l.diff = terminalRect{right.x + 1, contentY, iw, content}
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
		} else if content > 0 {
			l.agents = terminalRect{right.x + 1, contentY, iw, content}
			// The feed-only renderer clears pane-local hits. Keep the separate
			// roster's targets, which were laid out before the Activity pane.
			rosterHits := u.agents.hits
			body = u.agents.render(iw, content, now)
			u.agents.hits = rosterHits
			detail, state := u.agents.nativeTitle()
			title, label = nativeTitle(3, "Activity", detail, u.focus == 2), state
		}
		body = append(body[:min(len(body), content)], make([]string, max(0, content-len(body)))...)
		rules := map[int]string{}
		if content == 0 {
			title = nativeTitle(3, "Live", "", focused)
		}
		if dockRows > 0 && topDock {
			dock, err := u.renderIdleLive(ctx, right.w, dockRows, focused)
			if err != nil {
				return err
			}
			body = append(dock, body...)
		} else if dockRows > 0 {
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
	u.paintSelection(rows)
	var b strings.Builder
	b.WriteString(u.clipboard)
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
		u.clipboard = ""
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
			text = activityui.Dim + text + activityui.Undim + badge + " "
		}
		return text
	}
	diffBadge := ""
	if u.diffUnseen {
		diffBadge = activityui.Amber + "●" + activityui.Reset
	}
	responding, _ := u.agents.statusCounts(u.agents.roster())
	agentsBadge := ""
	if responding > 0 {
		agentsBadge = activityui.Amber + superscript(responding) + activityui.Reset
	}
	activityName := "Activity"
	if len(u.mainDock.Order) > 0 || len(u.agentDock.Order) > 0 {
		activityName = "Live"
	}
	for _, agent := range u.agents.agents {
		if agent.Name != "/root" {
			activityName = "Activity"
			break
		}
	}
	pair := activityui.Dim + "[" + activityui.Undim + tab(2, "Diff", diffBadge, u.diffOpen) + activityui.Dim + "│" + activityui.Undim +
		tab(3, activityName, "", !u.diffOpen) + activityui.Dim + "]" + activityui.Undim
	tabs := tab(1, "Main", "", true) + " " + pair + " " + tab(4, "Agents", agentsBadge, true)
	var hints terminalHints
	switch {
	case u.prefix:
		hints = terminalHints{{"ctrl+b 1-4", "focus", 0}, {"2/3", "diff or activity", 0}, {"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}
		return tabs + "  " + hints.render()
	case u.focus == 1:
		hints = terminalHints{{"s", "files", 0}, {"tab", "changes", 0}, {"[ ]", "hunks", 0}, {"a", "caller", 0}, {"r", "follow", 0}, {"?", "help", 0}}
		if u.diff.back.kind != 0 {
			hints = append(terminalHints{{"esc", "back", 0}}, hints...)
		}
	case u.focus == 2 && activityName == "Live":
		hints = terminalHints{{"^B 2", "diff", 0}, {"^B e", "next live", 0}}
	case u.focus == 2:
		hints = terminalHints{{"j/k", "scroll", 0}, {"n/p", "agent", 0}, {"o", "only", 0}, {"esc", "bottom", 0}, {"enter", "open", 0}}
	case u.focus == 3:
		hints = terminalHints{{"j/k", "agent", 0}, {"o", "only", 0}, {"esc", "back", 0}}
	default:
		hints = terminalHints{{"PgUp/PgDn", "scroll", 0}, {"/quit", "exits", 0}}
	}
	if u.mainDock.Live()+u.agentDock.Live() > 1 {
		hints = append(hints, terminalHint{"^B e", "next live", 0})
	}
	hints = append(hints, terminalHint{"^B 1-4", "panes", 0})
	return tabs + "  " + hints.render()
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
