package router

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// Live edits temporarily replace their caller's transcript, never another pane.
const (
	nativeDockMinimum = 1500 * time.Millisecond // Settling interval before a completed call retires.
	nativeDockReveal  = 300 * time.Millisecond  // Short edits never replace the transcript just to disappear.
	nativeRosterRows  = 4                       // Unfocused roster rows.
	nativeRosterShare = 0.4                     // Focused roster share of the screen.
	nativeFramedRows  = 8                       // Below this, panes drop their frames.
)

type nativePendingPreview struct {
	preview diffview.Preview
	since   time.Time
}

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
			return u.openEntry(u.agents, target.Seq)
		}
	}
	return false
}

// preview updates the shared dock by call ID, including caller-less completion markers.
func (u *terminalUI) preview(preview diffview.Preview) {
	dock := &u.liveDock
	dock.Retain = true
	if preview.Caller == "" && preview.Workspace != "" {
		preview.Caller = "/root"
	}
	// Already visible callers retain their burst, without another reveal delay.
	// A call completed or withdrawn before first reveal never opens a dock.
	if dock.Views[preview.ID] == nil && !slices.Contains(dock.Callers(), preview.Caller) {
		if preview.Complete || preview.Workspace == "" || preview.Status == "" && len(preview.Files) == 0 && preview.Input == "" {
			delete(u.livePending, preview.ID)
			return
		}
		if u.livePending == nil {
			u.livePending = make(map[string]nativePendingPreview)
		}
		pending, exists := u.livePending[preview.ID]
		if !exists {
			pending.since = time.Now()
		}
		pending.preview = preview
		u.livePending[preview.ID] = pending
		return
	}
	delete(u.livePending, preview.ID)
	dock.Update(preview)
}

// applyNativeDiff sends saved changes to the diff pane and live edit cards to
// the shared dock. All edits use the router preview and capture owners.
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
			u.liveDock = diffview.PreviewPane{}
			clear(u.livePending)
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

// animating keeps frames coming while previews reveal or completed calls expire.
func (u *terminalUI) animating(now time.Time) bool {
	for _, pending := range slices.SortedFunc(maps.Values(u.livePending), func(a, b nativePendingPreview) int {
		return a.since.Compare(b.since)
	}) {
		if now.Sub(pending.since) >= nativeDockReveal {
			u.liveDock.Update(pending.preview)
			delete(u.livePending, pending.preview.ID)
		}
	}
	changed := u.liveDock.ExpireBatches(now, nativeDockMinimum)
	return changed || len(u.liveDock.Order) > 0 || len(u.livePending) > 0
}

// nextLive cycles files in the focused caller's retained batch.
func (u *terminalUI) nextLive() {
	caller := "/root"
	if u.focus != 0 {
		caller = u.agents.selected
		if caller == "" || caller == "/root" {
			for _, name := range u.liveDock.Callers() {
				if name != "/root" {
					caller = name
					break
				}
			}
		}
	}
	u.liveDock.NextBatch(caller)
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
// diff or Activity on the right, one shared live dock, the fitted roster
// below, and a status bar for the focused pane.
func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	width, height := max(1, u.width), max(1, u.height)
	now := u.main.now()
	if u.focus == 1 || u.focus == 2 {
		u.journalOpen = false
	}
	u.agents.feedOnly, u.agents.focused = true, u.focus == 2
	u.liveDock.Motion.Enabled = true
	u.liveDock.Motion.Canvas = u.diff.theme.Canvas()
	if u.diff.backgrounded {
		u.liveDock.Motion.Canvas.Background = u.diff.background
	}
	mainEdit := !u.liveHidden && slices.Contains(u.liveDock.Callers(), "/root")
	u.agents.livePreviews = nil
	if u.diff != nil {
		u.agents.lineCounts = u.diff.callerCounts
		u.agents.netCounts = u.diff.netCounts
	}
	roster := u.agents.nativeRoster(width, u.rosterLimit(height), now, u.focus == 3)
	if height-1-len(roster) < nativeFramedRows {
		roster = nil // A short terminal keeps its rows for Main.
	}
	padding := 0
	if len(roster) > 0 && height-1-len(roster)-1 >= nativeFramedRows {
		padding = 1
	}
	top := max(0, height-1-len(roster)-padding)
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
		dockRows := 0
		if mainEdit && !u.main.keybindings && !u.main.picker.open {
			dockRows = ih
		}
		body, dockRect := u.main.mainFrame(iw, ih, dockRows)
		dockAt, dockRows := dockRect.y, dockRect.h
		rules := map[int]string{}
		if dockRows > 0 && dockAt+dockRows <= len(body) {
			dock, err := u.liveDock.RenderBatch(ctx, "/root", u.main.session.cwd, u.diff.theme, iw, dockRows, 15)
			if err != nil {
				return err
			}
			for i := 0; i < dockRows; i++ {
				body[dockAt+i] = ""
				if i < len(dock) {
					body[dockAt+i] = dock[i]
				}
			}
		}
		if dockRows > 0 {
			l.live = terminalRect{left.x + 1, left.y + 1 + dockAt, iw, dockRows}
			if !framed {
				l.live = terminalRect{left.x, left.y + dockAt, iw, dockRows}
			}
		}
		if !framed {
			l.codex = left
			draw(left, body)
		} else {
			l.codex = terminalRect{left.x + 1, left.y + 1, iw, ih}
			draw(left, nativeBox(left.w, left.h, nativeTitle(1, "Main", u.main.questionBadge(), u.focus == 0), u.main.mainHeaderRight(left.w, u.focus == 0), u.focus == 0, body, rules))
		}
	}
	if !framed && right.w > 0 && right.h > 0 && u.journalOpen {
		l.journal = right
		draw(right, u.main.journalView.render(u.main.journalTreeSnapshot(), right.w, right.h, true, u.focus == 4, u.main.view.painter.Theme))
	}
	if right.w >= 4 && right.h >= 3 && framed {
		iw, ih := right.w-2, right.h-2
		content := ih
		contentY := right.y + 1
		focused := u.focus == 1 || u.focus == 2 || u.focus == 4
		var body []string
		var title, label string
		if u.diffOpen {
			diffRows := content
			l.diff = terminalRect{right.x + 1, contentY, iw, diffRows}
			if u.diffScreen.Width() != iw || u.diffScreen.Height() != diffRows {
				u.diffScreen.Resize(iw, max(1, diffRows))
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
			mode := "saved · " + summary
			if !u.diff.diffMode {
				mode = "live proposals"
			}
			title, label = nativeTitle(2, "Diff", mode, u.focus == 1), state
			u.diffUnseen = false
		} else if content > 0 && u.journalOpen {
			l.journal = terminalRect{right.x + 1, contentY, iw, content}
			journal := u.main.journalTreeSnapshot()
			body = u.main.journalView.render(journal, iw, content, false, u.focus == 4, u.main.view.painter.Theme)
			label = u.main.journalPaneHints()
			// Counts drop their state names before the pane hints drop.
			for _, compact := range []bool{false, true} {
				var detail []string
				if namespace, _ := u.main.journalNamespaces(); namespace != "" {
					detail = append(detail, namespace)
				}
				if journal != nil {
					if counts := journalTitleCounts(journal.Items, compact); counts != "" {
						detail = append(detail, counts)
					}
				}
				title = nativeTitle(5, "Journal", strings.Join(detail, activityui.Dim+" · "+activityui.Undim), u.focus == 4)
				if ansi.StringWidth(title)+ansi.StringWidth(label)+5 <= right.w-2 {
					break
				}
			}
		} else if content > 0 {
			l.agents = terminalRect{right.x + 1, contentY, iw, content}
			// The feed-only renderer clears pane-local hits. Keep the separate
			// roster's targets, which were laid out before the Activity pane.
			rosterHits := u.agents.hits
			if !u.liveHidden {
				u.agents.livePreviews = make(map[string][]string)
				for _, caller := range u.liveDock.Callers() {
					if caller == "/root" || u.agents.only && caller != u.agents.selected {
						continue
					}
					preview, err := u.liveDock.RenderBatch(ctx, caller, u.main.session.cwd, u.diff.theme, iw-1, min(10, max(2, content/2)), min(10, max(2, content/2))-1)
					if err != nil {
						return err
					}
					u.agents.livePreviews[caller] = preview
				}
			}
			body = u.agents.render(iw, content, now)
			for row := range body {
				body[row] = activityui.AttachCopy(body[row], u.agents.copyRows[row])
			}
			u.agents.hits = rosterHits
			if u.main != nil {
				u.main.updateHistoryHint()
			}
			detail, state := u.agents.nativeTitle()
			title, label = nativeTitle(3, "Activity", detail, u.focus == 2), state
		}
		body = append(body[:min(len(body), content)], make([]string, max(0, content-len(body)))...)
		rules := map[int]string{}
		draw(right, nativeBox(right.w, right.h, title, label, focused, body, rules))
	}
	if len(roster) > 0 {
		draw(l.roster, roster)
	}
	if u.output != nil {
		u.paintOutput(rows[:height-1], width, height-1)
	}
	rows[height-1] += "\x1b[1G\x1b[0m" + ansi.Truncate(u.nativeStatus(), width, "")
	u.layout = l
	copyRows := make([][]activityui.CopySpan, len(rows))
	for y := range rows {
		rows[y], copyRows[y] = activityui.ExtractCopy(rows[y])
	}
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
	frame := b.String()
	if !u.faint {
		frame = activityui.FaintFallback(frame)
	}
	_, err := io.WriteString(out, frame)
	if err == nil {
		u.clipboard = ""
		u.paintedRows, u.paintedWidth = rows, u.width
		u.paintedCopy = copyRows
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
	u.paneTabs = [5]terminalRect{}
	if u.main != nil && u.main.replay != nil {
		return u.main.replay.bar(u.width)
	}
	var tabs strings.Builder
	tab := func(digit int, name, badge string, shown bool) {
		text := fmt.Sprintf(" %d %s", digit, name)
		switch {
		case u.focus == digit-1:
			text = "\x1b[7;1m" + text + badge + " \x1b[27;22m"
		case shown:
			text += badge + " "
		default:
			text = activityui.Dim + text + activityui.Undim + badge + " "
		}
		left := ansi.StringWidth(tabs.String())
		u.paneTabs[digit-1] = terminalRect{left, u.height - 1, max(0, min(ansi.StringWidth(text), u.width-left)), 1}
		tabs.WriteString(text)
	}
	diffBadge := ""
	if u.diffUnseen {
		diffBadge = activityui.Amber + " ●" + "\x1b[39m"
	}
	responding, _ := u.agents.statusCounts(u.agents.roster())
	agentsBadge := ""
	if responding > 0 {
		agentsBadge = activityui.Amber + superscript(responding) + "\x1b[39m"
	}
	tab(1, "Main", "", true)
	tabs.WriteString(" " + activityui.Dim + "[" + activityui.Undim)
	tab(2, "Diff", diffBadge, u.diffOpen)
	tabs.WriteString(activityui.Dim + "│" + activityui.Undim)
	tab(3, "Activity", "", !u.diffOpen && !u.journalOpen)
	tabs.WriteString(activityui.Dim + "│" + activityui.Undim)
	tab(5, "Journal", "", u.journalOpen)
	tabs.WriteString(activityui.Dim + "]" + activityui.Undim + " ")
	tab(4, "Agents", agentsBadge, true)
	if u.main != nil && u.main.interruptLocked {
		tabs.WriteString("  " + activityui.Amber + "Locked · /unlock" + activityui.Reset)
	}
	filter := "only"
	if u.agents.only {
		filter = "all"
	}
	var hints terminalHints
	switch {
	case u.prefix:
		hints = terminalHints{{"ctrl+b 1-5", "focus", 0}, {"2/3", "diff or activity", 0}, {"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}
		return tabs.String() + "  " + hints.render()
	case u.focus == 1:
		hints = terminalHints{{"s", "files", 0}, {"tab", "changes", 0}, {"[ ]", "hunks", 0}, {"a", "caller", 0}, {"r", "follow", 0}, {"?", "help", 0}}
		if u.diff.back.kind != 0 {
			hints = append(terminalHints{{"esc", "back", 0}}, hints...)
		}
	case u.focus == 2:
		hints = terminalHints{{"j/k", "scroll", 0}, {"n/p", "agent", 0}, {"a", filter, 0}, {"esc", "bottom", 0}, {"enter", "open", 0}}
	case u.focus == 4:
		// Expansion, details and namespace keys are in the pane title.
		hints = terminalHints{{"j/k", "select", 0}, {"enter", "open", 0}, {"c", "copy path", 0}, {"esc", "Main", 0}}
	case u.focus == 3:
		hints = terminalHints{{"j/k", "agent", 0}, {"a", filter, 0}, {"esc", "back", 0}}
	}
	if u.liveDock.Live() > 1 {
		hints = append(hints, terminalHint{"^B e", "next live", 0})
	}
	hints = append(hints, terminalHint{"^B 1-5", "panes", 0})
	return tabs.String() + "  " + hints.render()
}

// selectNativePane gives status-bar clicks and Ctrl-B digits the same action.
func (u *terminalUI) selectNativePane(pane int) {
	u.focus = pane
	if pane == 0 {
		return
	}
	u.autoActivity = false
	u.side = true
	switch pane {
	case 1:
		u.journalOpen, u.diffOpen = false, true
	case 2:
		u.journalOpen, u.diffOpen, u.activityOpen = false, false, true
	case 3:
		u.activityOpen = true
	case 4:
		u.journalOpen, u.diffOpen = true, false
	}
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
		u.autoActivity, u.journalOpen = false, false
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
