package router

import (
	"context"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/activitydetail"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

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
		receipt := &changepresentation.Receipt{Thread: preview.Thread, Calls: preview.HostItems, Text: preview.EditReceipt}
		for _, file := range preview.Files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			receipt.Pages = append(receipt.Pages, activitydetail.Page{ID: preview.ID + "/" + strconv.Itoa(len(receipt.Pages)), Verb: file.Action().Title(),
				Path: pathdisplay.ForWorkspace(preview.Workspace, path), Code: file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath)), Language: "diff"})
		}
		if len(receipt.Pages) == 0 {
			// Large live cards discard Files. Their receipt still carries bounded
			// observed hunks, including an explicit marker for omitted lines.
			for _, paragraph := range activitymodel.Paragraphs(receipt.Text) {
				block := activityui.Block{Content: activitymodel.ParseOperation(paragraph)}
				if block.Lang == "diff" && block.Code != "" {
					block.Path = block.EditPath()
					receipt.Pages = append(receipt.Pages, activitydetail.Page{ID: preview.ID + "/" + strconv.Itoa(len(receipt.Pages)), Verb: block.Verb, Path: block.Path, Code: block.Code, Language: block.Lang})
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
			budget -= 1024 + len(pending.Thread) + len(pending.Text)
			for _, call := range pending.Calls {
				budget -= 32 + len(call)
			}
			for _, page := range pending.Pages {
				budget -= 1024 + len(page.Path) + len(page.Code)
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

// A closed subscription can still have queued scope barriers. Reject them
// before they can authorize previews or hydrate the replacement attachment.
// Failed retained loading leaves preview delivery active without requesting
// another durable barrier after every event.
func (u *terminalUI) admitReviewEvents(ctx context.Context, sub *reviewSubscription, events ...reviewEvent) bool {
	if sub.Invalidated() {
		return false
	}
	for _, event := range events {
		u.applyDiff(ctx, event)
	}
	return u.diffFailure != "" || u.reviewLoader == nil || !u.reviewLoader.resync
}

// applyDiff sends saved changes to the diff pane and live edit cards to
// the shared dock. All edits use the router preview and capture owners.
func (u *terminalUI) applyDiff(ctx context.Context, event reviewEvent) {
	switch event.Kind {
	case "preview":
		preview := event.Preview
		scope := u.saved.scope
		if u.reviewLoader != nil {
			scope = u.reviewScope
		}
		if preview == nil ||
			preview.Workspace != "" && !scope.Workspaces[preview.Workspace][preview.Thread] {
			return
		}
		u.preview(*preview, event.Resync)
		return
	case "scope":
		if event.Scope != nil {
			u.reviewScope = *event.Scope
		}
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
	if u.reviewLoader != nil {
		if err := u.reviewLoader.enqueue(event); err != nil {
			u.diffFailure = err.Error()
			u.publishReview()
		}
		return
	}
	if changed, err := u.saved.applyEvent(ctx, event); err != nil {
		u.diffFailure = err.Error()
		u.publishReview()
	} else if changed {
		u.publishReview()
	}
	if u.main != nil && (event.Kind == "scope" || event.Kind == "change") {
		u.main.applyCapturedEdits()
	}
}

func (u *terminalUI) loadedReview(result savedReviewResult) {
	accepted, err := u.reviewLoader.accept(result)
	if err != nil {
		u.diffFailure = err.Error()
		u.publishReview()
	} else if accepted {
		workspace := u.saved.workspace
		u.saved = u.reviewLoader.state
		u.saved.workspace = workspace
		u.publishReview()
		if u.main != nil {
			u.main.applyCapturedEdits()
		}
	}
}

func (u *terminalUI) publishReview() {
	snapshot := u.saved.data
	if u.main != nil {
		u.main.reviewSource.PublishReview(u.main.session.Scope(), snapshot, u.saved.workspace, u.saved.coverage, u.diffFailure)
		if u.preparePane != nil {
			u.preparePane(u.main.session.Scope())
		}
	}
	u.agents.Model.LineCounts, u.agents.Model.NetCounts = snapshot.Publication.Counts.Callers, snapshot.Publication.Counts.Net
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
	u.main.mainView.PromptRect = terminalsession.Rect{}
	width, height := max(1, u.width), max(1, u.height)
	now := u.main.now()
	u.agents.FeedOnly, u.agents.Focused = true, u.Focus == 3
	u.liveDock.Motion.Enabled = true
	u.liveDock.Motion.Canvas = u.Theme.Canvas()
	if u.agents.Painter.Colors.HasBackground {
		u.liveDock.Motion.Canvas.Background = u.agents.Painter.Colors.Background
	}
	caller := u.main.transcriptCaller()
	mainEdit := !u.liveHidden && slices.Contains(u.liveDock.Callers(), caller)
	u.agents.LivePreviews = nil

	var roster []string
	if u.RosterLabel != "" {
		roster = u.agents.NativeRoster(width, u.Panes.RosterLimit(height), now, u.Focus == 3)
	} else {
		u.agents.Hits = nil
	}
	plan := u.Shell.Plan(width, height, len(roster))
	content := terminalsession.ShellContent{Roster: roster, MainName: "Main"}
	if plan.Left.W >= 4 && plan.Left.H >= 1 {
		iw, ih := plan.Layout.Main.W, plan.Layout.Main.H
		dockRows := 0
		if mainEdit && !u.main.keybindings && !u.main.picker.Open {
			dockRows = ih
		}
		rosterHits := u.agents.Hits
		content.Main, content.Dock = u.main.mainFrame(iw, ih, dockRows)
		u.agents.Hits = rosterHits
		dockAt, dockRows := content.Dock.Y, content.Dock.H
		if dockRows > 0 && dockAt+dockRows <= len(content.Main) {
			dock, err := u.liveDock.RenderBatch(ctx, caller, u.main.session.cwd, u.Theme, iw, dockRows, 15)
			if err != nil {
				return err
			}
			for i := 0; i < dockRows; i++ {
				content.Main[dockAt+i] = ""
				if i < len(dock) {
					content.Main[dockAt+i] = dock[i]
				}
			}
		}
		if plan.Framed {
			content.MainDetail = u.main.mainTitleDetail()
			if set := u.main.view.ActiveSkills()["Main"]; set != nil {
				content.MainSkills = activityui.SkillLabel(set)
				if u.main.view.SkillsHover == "Main" {
					content.MainDetail = strings.TrimSuffix(content.MainDetail, content.MainSkills) + activityui.Underline(content.MainSkills)
				}
			}
			content.MainRight = u.main.mainHeaderRight(plan.Left.W, u.Focus == 0)
			if u.agents.displayingChild() {
				content.MainName, content.MainDetail = "Thread", u.agents.Painter.Agent(u.agents.displayedCaller())
				content.MainSkills = ""
				u.main.updateHistoryHint()
				content.MainRight = strings.TrimSpace(scrollLabel(u.agents) + " " + u.agents.HistoryHint)
			}
		}
	}
	if plan.Right.W >= 4 && plan.Right.H >= 1 {
		var err error
		content.Diff, err = u.diffScreen.Paint(ctx, u.Auxiliary, plan.Layout.Diff.W, plan.Layout.Diff.H)
		if err != nil {
			content.Diff = []string{u.AuxiliaryLabel + " unavailable", textsanitize.Safe(err.Error(), false)}
		}
		content.DiffSummary, content.DiffState = u.Auxiliary.Title()
		u.diffUnseen = false
	}
	content.Status = u.shellStatus()
	rows := u.Shell.Compose(plan, content)
	if u.output != nil {
		u.paintOutput(rows[:height-1], width, height-1)
	}
	copyRows := make([][]terminalui.CopySpan, len(rows))
	for y := range rows {
		rows[y], copyRows[y] = terminalui.ExtractCopy(rows[y])
	}
	u.paintSelection(rows)
	if err := u.frame.Write(out, rows, copyRows, u.width, u.faint); err != nil {
		return err
	}
	// Composing Main does not show a prompt covered by a dialog or frozen selection.
	var selected terminalsession.Rect
	if u.selection != nil {
		selected = u.selection.Rect
	}
	visible := u.Shell.PromptVisible(u.main.mainView.PromptRect, u.output != nil, selected)
	if visible {
		if u.main.activeQuestionCall() != nil {
			u.main.questions.painted = true
		}
		if u.main.approvals.Open {
			u.main.approvals.painted = true
		}
	}
	return nil
}

// shellStatus binds owner facts to the terminal's status view.
func (u *terminalUI) shellStatus() terminalsession.ShellStatus {
	if u.main != nil && u.main.replay != nil {
		return terminalsession.ShellStatus{Playback: true, PlaybackBar: u.main.replay.bar(u.width)}
	}
	responding, _ := u.agents.StatusCounts(u.agents.View.Roster())
	in := terminalsession.ShellStatus{
		Child: u.agents.displayingChild(), Responding: responding,
		DiffUnseen: u.diffUnseen, Prefix: u.prefix, LiveCount: u.liveDock.Live(),
	}
	if u.main != nil {
		in.Locked = u.main.interruptLocked
		if u.Focus == 0 && !u.prefix {
			in.Expansion = u.main.transcriptView().Expansion
		}
	}
	return in
}

// selectNativePane gives status-bar clicks and Ctrl-B digits the same action.
func (u *terminalUI) selectNativePane(pane int) {
	if pane == 0 {
		u.agents.displayThread("/root")
	}
	u.Shell.Select(pane)
	u.selection = nil
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
