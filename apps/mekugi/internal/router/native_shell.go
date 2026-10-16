package router

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

// Live edits temporarily replace their caller's transcript, never another pane.
const (
	nativeDockMinimum = 1500 * time.Millisecond // Settling interval before a completed call retires.
	nativeDockReveal  = 300 * time.Millisecond  // Short edits never replace the transcript just to disappear.
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
					block.Path = block.EditPath()
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
			preview.Workspace != "" && !u.saved.scope.Workspaces[preview.Workspace][preview.Thread] {
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
		u.diffUnseen = u.diffUnseen || !u.DiffOpen
	}
	if u.diffFailure != "" {
		return
	}
	if changed, err := u.saved.applyEvent(ctx, event); err != nil {
		u.diffFailure = err.Error()
	} else if changed {
		if event.Kind == "coverage" {
			u.diff.SetCoverage(u.saved.coverage)
		} else {
			u.diff.Update(u.saved.data.files(), u.saved.coverage)
		}
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

func (u *appServerUI) transcriptCaller() string {
	return u.sessionPresentation().View().Displayed.Caller
}

func (u *appServerUI) composerReadOnly() bool {
	return u.sessionPresentation().View().ReadOnly != sessionpresentation.Editable
}

// transcriptView selects display state without changing the input or execution owner.
func (u *appServerUI) transcriptView() *activityAdapter {
	view := u.sessionPresentation().View()
	if u.agents != nil && view.Displayed.ID != view.Main.ID {
		u.agents.Conversation = true
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
	u.agents.FeedOnly, u.agents.Focused = true, u.Focus == 3
	u.liveDock.Motion.Enabled = true
	u.liveDock.Motion.Canvas = u.diff.Theme().Canvas()
	if u.agents.Painter.Colors.HasBackground {
		u.liveDock.Motion.Canvas.Background = u.agents.Painter.Colors.Background
	}
	caller := u.main.transcriptCaller()
	mainEdit := !u.liveHidden && slices.Contains(u.liveDock.Callers(), caller)
	u.agents.LivePreviews = nil
	if u.diff != nil {
		u.agents.LineCounts, u.agents.NetCounts = u.diff.Counts()
	}
	roster := u.agents.NativeRoster(width, u.Panes.RosterLimit(height), now, u.Focus == 3)
	l, left, right, framed, rosterRows := u.Panes.Geometry(width, height, len(roster))
	roster = roster[:rosterRows]
	rows := make([]string, height)
	draw := func(r terminalsession.Rect, lines []string) {
		for i, line := range lines {
			if y := r.Y + i; y >= 0 && y < len(rows) && i < r.H {
				rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", r.X+1, line)
			}
		}
	}
	if left.W >= 4 && left.H >= 1 {
		iw, ih := l.Main.W, l.Main.H
		dockRows := 0
		if mainEdit && !u.main.keybindings && !u.main.picker.Open {
			dockRows = ih
		}
		rosterHits := u.agents.Hits
		body, dockRect := u.main.mainFrame(iw, ih, dockRows)
		u.agents.Hits = rosterHits
		dockAt, dockRows := dockRect.Y, dockRect.H
		rules := map[int]string{}
		if dockRows > 0 && dockAt+dockRows <= len(body) {
			dock, err := u.liveDock.RenderBatch(ctx, caller, u.main.session.cwd, u.diff.Theme(), iw, dockRows, 15)
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
			l.Live = terminalsession.Rect{left.X + 1, left.Y + 1 + dockAt, iw, dockRows}
			if !framed {
				l.Live = terminalsession.Rect{left.X, left.Y + dockAt, iw, dockRows}
			}
		}
		if !framed {
			draw(left, body)
		} else {
			detail := u.main.mainTitleDetail()
			if set := u.main.view.ActiveSkills()["Main"]; set != nil && u.main.view.SkillsHover == "Main" {
				detail = strings.TrimSuffix(detail, activityui.SkillLabel(set)) + activityui.Underline(activityui.SkillLabel(set))
			}
			name := "Main"
			if u.agents.displayingChild() {
				name, detail = "Thread", u.agents.Painter.Agent(u.agents.displayedCaller())
			}
			title := terminalui.PaneTitle(1, name, detail, u.Focus == 0)
			// The skill count ends the title, after the frame corner and a space.
			// A truncated title has no reliable target.
			if set := u.main.view.ActiveSkills()["Main"]; !u.agents.displayingChild() && set != nil && strings.HasSuffix(ansi.Strip(detail), activityui.SkillLabel(set)) && ansi.StringWidth(title)+5 <= left.W {
				end := left.X + 2 + ansi.StringWidth(title)
				l.MainSkills = terminalsession.Rect{end - ansi.StringWidth(activityui.SkillLabel(set)), left.Y, ansi.StringWidth(activityui.SkillLabel(set)), 1}
			}
			headerRight := u.main.mainHeaderRight(left.W, u.Focus == 0)
			if u.agents.displayingChild() {
				u.main.updateHistoryHint()
				headerRight = strings.TrimSpace(scrollLabel(u.agents) + " " + u.agents.HistoryHint)
			}
			draw(left, terminalui.PaneBox(left.W, left.H, title, headerRight, u.Focus == 0, body, rules))
		}
	}
	if right.W >= 4 && right.H >= 1 {
		iw, content := l.Diff.W, l.Diff.H
		focused := u.Focus == 1
		var body []string
		var title, label string
		if u.diffFailure == "" {
			var err error
			body, err = u.diffScreen.Paint(ctx, u.diff, iw, content)
			if err != nil {
				u.diffFailure = err.Error()
			}
		}
		if u.diffFailure != "" {
			body = []string{"Diff unavailable", textsanitize.Safe(u.diffFailure, false), "Use mchanges to review captured edits."}
		}
		summary, state := u.diff.Title()
		title, label = terminalui.PaneTitle(2, "Diff", "saved · "+summary, u.Focus == 1), state
		u.diffUnseen = false
		body = append(body[:min(len(body), content)], make([]string, max(0, content-len(body)))...)
		if framed {
			draw(right, terminalui.PaneBox(right.W, right.H, title, label, focused, body, nil))
		} else {
			draw(right, body)
		}
	}
	if len(roster) > 0 {
		draw(l.Roster, roster)
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
	return u.frame.Write(out, rows, copyRows, u.width, u.faint)
}

// nativeStatus shows view selectors and only the focused view's keys.
func (u *terminalUI) nativeStatus() string {
	u.paneTabs = [4]terminalsession.Rect{}
	if u.main != nil && u.main.replay != nil {
		return u.main.replay.bar(u.width)
	}
	var tabs strings.Builder
	tab := func(digit int, name, badge string, shown bool) {
		text := fmt.Sprintf(" %d %s", digit, name)
		switch {
		case (u.Focus == digit-1 && digit != 3 || digit == 3 && u.Focus == 3) && (digit != 1 || !u.agents.displayingChild()):
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
		u.paneTabs[pane] = terminalsession.Rect{left, u.height - 1, max(0, min(ansi.StringWidth(text), u.width-left)), 1}
		tabs.WriteString(text)
	}
	diffBadge := ""
	if u.diffUnseen {
		diffBadge = terminalui.Amber + " ●" + "\x1b[39m"
	}
	responding, _ := u.agents.StatusCounts(u.agents.View.Roster())
	agentsBadge := ""
	if responding > 0 {
		agentsBadge = terminalui.Amber + superscript(responding) + "\x1b[39m"
	}
	tab(1, "Main", "", !u.agents.displayingChild())
	tab(2, "Diff", diffBadge, u.DiffOpen)
	tab(3, "Agents", agentsBadge, true)
	if u.main != nil && u.main.interruptLocked {
		tabs.WriteString("  " + terminalui.Amber + "Locked · /unlock" + terminalui.Reset)
	}
	filter := "child"
	if u.agents.displayingChild() {
		filter = "Main"
	}
	var hints terminalui.Hints
	switch {
	case u.prefix:
		hints = terminalui.Hints{{"ctrl+b 1-3", "select", 0}, {"2", "toggle diff", 0}, {"o", "older child history", 0}, {"e", "next live", 0}, {"←→", "resize", 0}, {"PgUp/PgDn", "history", 0}}
		return tabs.String() + "  " + hints.Render()
	case u.Focus == 1:
		hints = terminalui.Hints{{"s", "files", 0}, {"tab", "changes", 0}, {"[ ]", "hunks", 0}, {"a", "caller", 0}, {"?", "help", 0}}
		if u.diff.Back() {
			hints = append(terminalui.Hints{{"esc", "back", 0}}, hints...)
		}
	case u.Focus == 3:
		hints = terminalui.Hints{{"j/k", "agent", 0}, {"a", filter, 0}, {"esc", "back", 0}}
	}
	if u.liveDock.Live() > 1 {
		hints = append(hints, terminalui.Hint{"^B e", "next live", 0})
	}
	hints = append(hints, terminalui.Hint{"^B 1-3", "views", 0})
	if u.Focus == 0 {
		expansion := u.main.transcriptView().Expansion
		hints = append(hints, terminalui.Hint{"^E", [3]string{"expand events", "expand all", "default"}[expansion], 0})
	}
	return tabs.String() + "  " + hints.Render()
}

// selectNativePane gives status-bar clicks and Ctrl-B digits the same action.
func (u *terminalUI) selectNativePane(pane int) {
	if pane == 0 {
		u.agents.displayThread("/root")
	}
	u.Panes.Select(pane)
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
	u.agents.applyDisplayIntent()
	if u.main != nil && (u.agents.displayingChild() != only || u.agents.Selected != selected) {
		u.selection = nil
		u.agents.Runs = nil
	}
}

// flushEscape sends a lone Esc once no sequence followed it.
func (u *terminalUI) flushEscape() error {
	if !u.input.Escape(time.Now()) {
		return nil
	}
	return u.send("\x1b")
}
