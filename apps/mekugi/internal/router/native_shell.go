package router

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
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
	preview previewmodel.Preview
	since   time.Time
}

// A Main excerpt carries an Activity identity, not a nearby question or run.
func (u *terminalUI) openActivityReply(seq uint64) bool {
	v := u.main.view
	for _, entry := range v.Entries {
		if entry.Source.Seq != seq || entry.Source.activitySeq == 0 {
			continue
		}
		for _, target := range u.agents.Entries {
			if target.Source.Seq != entry.Source.activitySeq {
				continue
			}
			return u.openEntry(u.agents, target.Source.Seq)
		}
	}
	return false
}

// preview updates the shared dock by call ID, including caller-less completion markers.
func (u *terminalUI) preview(preview previewmodel.Preview, restored bool) {
	if u.main != nil && preview.Complete && len(preview.HostItems) > 0 {
		receipt := &capturedActivityEdit{thread: preview.Thread, calls: preview.HostItems, text: preview.EditReceipt}
		for _, file := range preview.Files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			receipt.pages = append(receipt.pages, activityui.Block{Kind: "op", Verb: file.Action().Title(),
				Path: pathdisplay.ForWorkspace(preview.Workspace, path), Code: file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath)), Lang: "diff", Fenced: true})
		}
		if len(receipt.pages) == 0 {
			// Large live cards discard Files. Their receipt still carries bounded
			// observed hunks, including an explicit marker for omitted lines.
			for _, paragraph := range activitymodel.Paragraphs(receipt.text) {
				block := activityui.Block{Content: activitymodel.ParseOperation(paragraph)}
				if block.Lang == "diff" && block.Code != "" {
					block.Path = activityui.EditPath(block)
					receipt.pages = append(receipt.pages, block)
				}
			}
		}
		u.main.pendingEditReceipts = append(u.main.pendingEditReceipts, receipt)
		u.main.applyCapturedEdits()
		// Host rows can lag several preview mailbox drains. Bound unmatched
		// projections by memory, not one mailbox's count. Charge metadata too,
		// so tiny receipts cannot grow without a bound. These are not durable
		// changes or evidence of the whole command's success.
		budget := 32 * (48 << 10)
		for i := len(u.main.pendingEditReceipts) - 1; i >= 0; i-- {
			pending := u.main.pendingEditReceipts[i]
			budget -= 1024 + len(pending.thread) + len(pending.text)
			for _, call := range pending.calls {
				budget -= 32 + len(call)
			}
			for _, page := range pending.pages {
				budget -= 1024 + len(page.Path) + len(page.Code) + len(page.Label)
			}
			if budget < 0 {
				u.main.pendingEditReceipts = slices.Delete(u.main.pendingEditReceipts, 0, i+1)
				break
			}
		}
	}
	dock := &u.liveDock
	// Restoration may carry receipts for expired calls, but cannot reopen them.
	if restored && preview.Complete && dock.Views[preview.ID] == nil {
		delete(u.livePending, preview.ID)
		return
	}
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

// applyDiff sends saved changes to the diff pane and live edit cards to
// the shared dock. All edits use the router preview and capture owners.
func (u *terminalUI) applyDiff(ctx context.Context, event reviewEvent) {
	switch event.Kind {
	case "preview":
		preview := event.Preview
		if preview == nil ||
			preview.Workspace != "" && !u.diff.scope.Workspaces[preview.Workspace][preview.Thread] {
			return
		}
		u.preview(*preview, event.Resync)
		return
	case "scope":
		if event.Resync && event.Scope != nil {
			retained := func(preview previewmodel.Preview, complete bool) bool {
				return event.Scope.Workspaces[preview.Workspace][preview.Thread] &&
					(complete || slices.Contains(event.PreviewIDs, preview.ID))
			}
			u.liveDock.Order = slices.DeleteFunc(u.liveDock.Order, func(id string) bool {
				view := u.liveDock.Views[id]
				if retained(view.Current, view.Complete) {
					return false
				}
				delete(u.liveDock.Views, id)
				return true
			})
			for id, pending := range u.livePending {
				if !retained(pending.preview, false) {
					delete(u.livePending, id)
				}
			}
		}
	case "coverage":
		if strings.HasPrefix(event.Status, "RECONNECTING:") {
			u.liveDock = previewview.PreviewPane{}
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
		return " \x1b[39m" + label + terminalui.Reset + color + " "
	}
	l, r := segment(left), segment(right)
	if ansi.StringWidth(l)+ansi.StringWidth(r)+1 > inner {
		r = ""
	}
	if ansi.StringWidth(l)+1 > inner {
		l = ansi.Truncate(l, max(0, inner-2), "…") + terminalui.Reset + color + " "
	}
	gap := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r))
	return color + open + l + strings.Repeat(fill, gap) + r + close + terminalui.Reset
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
		lines = append(lines, color+"│"+terminalui.Reset+text+strings.Repeat(" ", max(0, width-2-ansi.StringWidth(text)))+color+"│"+terminalui.Reset)
	}
	return append(lines, color+"└"+strings.Repeat("─", width-2)+"┘"+terminalui.Reset)
}

// nativeTitle labels a pane with its Ctrl-B digit.
func nativeTitle(digit int, name, detail string, focused bool) string {
	label := fmt.Sprintf("%d %s", digit, name)
	if focused {
		label = nativeBorder(true) + "\x1b[1m" + label + "\x1b[22m"
	} else {
		label = terminalui.Dim + label + terminalui.Undim
	}
	if detail != "" {
		label += terminalui.Dim + " · " + terminalui.Undim + detail
	}
	return label
}

func (u *appServerUI) transcriptCaller() string {
	if u.composerReadOnly() {
		return u.agents.selected
	}
	return "/root"
}

func (u *appServerUI) composerReadOnly() bool {
	return u.agents != nil && u.agents.only
}

// transcriptView selects display state without changing the input or execution owner.
func (u *appServerUI) transcriptView() *liveActivityView {
	if u.agents != nil && u.agents.only && u.agents.selected != "" {
		u.agents.conversation = true
		return u.agents
	}
	return u.view
}

// paintNative lays out the app-server shell: Main on the left, the saved
// optional saved Diff on the right, a caller-local live dock, the fitted roster
// below, and a status bar for the focused pane.
func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	width, height := max(1, u.width), max(1, u.height)
	now := u.main.now()
	u.agents.feedOnly, u.agents.focused = true, u.focus == 3
	u.liveDock.Motion.Enabled = true
	u.liveDock.Motion.Canvas = u.diff.theme.Canvas()
	if u.diff.backgrounded {
		u.liveDock.Motion.Canvas.Background = u.diff.background
	}
	caller := u.main.transcriptCaller()
	mainEdit := !u.liveHidden && slices.Contains(u.liveDock.Callers(), caller)
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
	wide := u.diffOpen && width >= 100 && top >= 12
	switch {
	case wide:
		split := u.split
		if split == 0 {
			split = width / 2
		}
		split = min(max(40, split), width-40)
		left, right = terminalRect{0, 0, split, top}, terminalRect{split, 0, width - split, top}
		l.vertical = split
	case !u.diffOpen || u.focus != 1:
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
		rosterHits := u.agents.hits
		body, dockRect := u.main.mainFrame(iw, ih, dockRows)
		u.agents.hits = rosterHits
		dockAt, dockRows := dockRect.y, dockRect.h
		rules := map[int]string{}
		if dockRows > 0 && dockAt+dockRows <= len(body) {
			dock, err := u.liveDock.RenderBatch(ctx, caller, u.main.session.cwd, u.diff.theme, iw, dockRows, 15)
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
			detail := u.main.mainTitleDetail()
			if set := u.main.view.ActiveSkills()["Main"]; set != nil && u.main.view.skillsHover == "Main" {
				detail = strings.TrimSuffix(detail, skillLabel(set)) + activityui.Underline(skillLabel(set))
			}
			name := "Main"
			if u.agents.only {
				name, detail = "Thread", u.agents.painter.Agent(u.agents.selected)
			}
			title := nativeTitle(1, name, detail, u.focus == 0)
			// The skill count ends the title, after the frame corner and a space.
			// A truncated title has no reliable target.
			if set := u.main.view.ActiveSkills()["Main"]; !u.agents.only && set != nil && strings.HasSuffix(ansi.Strip(detail), skillLabel(set)) && ansi.StringWidth(title)+5 <= left.w {
				end := left.x + 2 + ansi.StringWidth(title)
				l.mainSkills = terminalRect{end - ansi.StringWidth(skillLabel(set)), left.y, ansi.StringWidth(skillLabel(set)), 1}
			}
			headerRight := u.main.mainHeaderRight(left.w, u.focus == 0)
			if u.agents.only {
				u.main.updateHistoryHint()
				headerRight = strings.TrimSpace(scrollLabel(u.agents) + " " + u.agents.historyHint)
			}
			draw(left, nativeBox(left.w, left.h, title, headerRight, u.focus == 0, body, rules))
		}
	}
	if right.w >= 4 && right.h >= 1 {
		inset := 0
		if framed {
			inset = 1
		}
		iw, ih := right.w-2*inset, right.h-2*inset
		content := ih
		contentY := right.y + inset
		focused := u.focus == 1
		var body []string
		var title, label string
		diffRows := content
		l.diff = terminalRect{right.x + inset, contentY, iw, diffRows}
		if u.diffScreen.Emulator == nil {
			u.diffScreen.Emulator = vt.NewEmulator(iw, max(1, diffRows))
			u.diff.dirty = true
		} else if u.diffScreen.Width() != iw || u.diffScreen.Height() != diffRows {
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
			body = []string{"Diff unavailable", textsanitize.Safe(u.diffFailure, false), "Use mchanges to review captured edits."}
		} else {
			body = strings.Split(u.diffScreen.Render(), "\n")
		}
		summary, state := u.diff.nativeTitle()
		title, label = nativeTitle(2, "Diff", "saved · "+summary, u.focus == 1), state
		u.diffUnseen = false
		body = append(body[:min(len(body), content)], make([]string, max(0, content-len(body)))...)
		if framed {
			draw(right, nativeBox(right.w, right.h, title, label, focused, body, nil))
		} else {
			draw(right, body)
		}
	}
	if len(roster) > 0 {
		draw(l.roster, roster)
	}
	if u.output != nil {
		u.paintOutput(rows[:height-1], width, height-1)
	}
	rows[height-1] += "\x1b[1G\x1b[0m" + ansi.Truncate(u.nativeStatus(), width, "")
	u.layout = l
	copyRows := make([][]terminalui.CopySpan, len(rows))
	for y := range rows {
		rows[y], copyRows[y] = terminalui.ExtractCopy(rows[y])
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
		frame = terminalui.FaintFallback(frame)
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

// nativeStatus shows view selectors and only the focused view's keys.
func (u *terminalUI) nativeStatus() string {
	u.paneTabs = [4]terminalRect{}
	if u.main != nil && u.main.replay != nil {
		return u.main.replay.bar(u.width)
	}
	var tabs strings.Builder
	tab := func(digit int, name, badge string, shown bool) {
		text := fmt.Sprintf(" %d %s", digit, name)
		switch {
		case (u.focus == digit-1 && digit != 3 || digit == 3 && u.focus == 3) && (digit != 1 || !u.agents.only):
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
		u.paneTabs[pane] = terminalRect{left, u.height - 1, max(0, min(ansi.StringWidth(text), u.width-left)), 1}
		tabs.WriteString(text)
	}
	diffBadge := ""
	if u.diffUnseen {
		diffBadge = terminalui.Amber + " ●" + "\x1b[39m"
	}
	responding, _ := u.agents.statusCounts(u.agents.Roster())
	agentsBadge := ""
	if responding > 0 {
		agentsBadge = terminalui.Amber + superscript(responding) + "\x1b[39m"
	}
	tab(1, "Main", "", !u.agents.only)
	tab(2, "Diff", diffBadge, u.diffOpen)
	tab(3, "Agents", agentsBadge, true)
	if u.main != nil && u.main.interruptLocked {
		tabs.WriteString("  " + terminalui.Amber + "Locked · /unlock" + terminalui.Reset)
	}
	filter := "child"
	if u.agents.only {
		filter = "Main"
	}
	var hints terminalHints
	switch {
	case u.prefix:
		hints = terminalHints{{"ctrl+b 1-3", "select", 0}, {"2", "toggle diff", 0}, {"o", "older child history", 0}, {"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}
		return tabs.String() + "  " + hints.render()
	case u.focus == 1:
		hints = terminalHints{{"s", "files", 0}, {"tab", "changes", 0}, {"[ ]", "hunks", 0}, {"a", "caller", 0}, {"?", "help", 0}}
		if u.diff.back.kind != 0 {
			hints = append(terminalHints{{"esc", "back", 0}}, hints...)
		}
	case u.focus == 3:
		hints = terminalHints{{"j/k", "agent", 0}, {"a", filter, 0}, {"esc", "back", 0}}
	}
	if u.liveDock.Live() > 1 {
		hints = append(hints, terminalHint{"^B e", "next live", 0})
	}
	hints = append(hints, terminalHint{"^B 1-3", "views", 0})
	if u.focus == 0 {
		expansion := u.main.transcriptView().expansion
		hints = append(hints, terminalHint{"^E", [3]string{"expand events", "expand all", "default"}[expansion], 0})
	}
	return tabs.String() + "  " + hints.render()
}

// selectNativePane gives status-bar clicks and Ctrl-B digits the same action.
func (u *terminalUI) selectNativePane(pane int) {
	switch pane {
	case 0:
		u.agents.only = false
		u.focus = 0
	case 1:
		u.diffOpen = !u.diffOpen
		u.focus = 0
		if u.diffOpen {
			u.focus = 1
		}
	case 3:
		u.focus = 3
	}
	u.selection = nil
}

func superscript(n int) string {
	digits := []rune("⁰¹²³⁴⁵⁶⁷⁸⁹")
	var b strings.Builder
	for _, r := range fmt.Sprint(n) {
		b.WriteRune(digits[r-'0'])
	}
	return b.String()
}

// showRosterPick switches presentation only; Main retains its composer and host identity.
func (u *terminalUI) showRosterPick(only bool, selected string) {
	if u.main != nil && (u.agents.only != only || u.agents.selected != selected) {
		u.selection = nil
		u.agents.runs = nil
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
