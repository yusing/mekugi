package router

import (
	"context"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// A Main excerpt carries an Activity identity, not a nearby question or run.
func (u *terminalUI) openActivityReply(seq uint64) bool {
	v := u.main.source
	for _, entry := range v.Entries {
		if entry.Source.Seq != seq || entry.Source.ActivitySeq == 0 {
			continue
		}
		for _, target := range u.main.childSource.Entries {
			if target.Source.Seq != entry.Source.ActivitySeq {
				continue
			}
			return u.openEntry(u.agents, target.Source.Seq)
		}
	}
	return false
}

func (u *appServerUI) transcriptCaller() string {
	return u.sessionPresentation().View().Displayed.Caller
}

func (u *appServerUI) composerReadOnly() bool {
	return u.sessionPresentation().View().ReadOnly != sessionpresentation.Editable
}

// transcriptView selects display state without changing the input or execution owner.
func (u *appServerUI) transcriptView() *activityui.View {
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
	promptReceipt := u.main.prompts.Update(u.main.sessionPresentation().View())
	u.main.mainView.PromptRect = terminalsession.Rect{}
	width, height := max(1, u.width), max(1, u.height)
	now := u.main.now()
	u.agents.FeedOnly, u.agents.Focused = true, u.Focus == 3
	u.liveDock.Motion.Enabled = true
	u.liveDock.Motion.Now = now
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
			dock, err := u.liveDock.RenderBatch(ctx, caller, u.main.cwd, u.Theme, iw, dockRows, 15)
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
			if set := u.main.source.ActiveSkills()["Main"]; set != nil {
				content.MainSkills = activityui.SkillLabel(set)
				if u.main.view.SkillsHover == "Main" {
					content.MainDetail = strings.TrimSuffix(content.MainDetail, content.MainSkills) + activityui.Underline(content.MainSkills)
				}
			}
			content.MainRight = u.main.mainHeaderRight(plan.Left.W, u.Focus == 0)
			if u.main.sessionPresentation().DisplayingChild() {
				content.MainName, content.MainDetail = "Thread", u.agents.Painter.Agent(u.main.sessionPresentation().View().Displayed.Caller)
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
		u.output.Paint(rows[:height-1], width, height-1)
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
		u.main.prompts.Delivered(promptReceipt)
	}

	return nil
}

// shellStatus binds owner facts to the terminal's status view.
func (u *terminalUI) shellStatus() terminalsession.ShellStatus {
	if u.main != nil && u.main.replay != nil {
		return terminalsession.ShellStatus{Playback: true, PlaybackBar: u.main.replay.bar(u.width)}
	}
	responding, _ := u.agents.StatusCounts(u.agents.Roster())
	in := terminalsession.ShellStatus{
		Child: u.main.sessionPresentation().DisplayingChild(), Responding: responding,
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
		view := u.main.sessionPresentation().View()
		u.main.displaySession(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main})
	}
	u.Shell.Select(pane)
	u.selection = nil
}

// showRosterPick switches presentation only; Main retains its composer and host identity.
func (u *terminalUI) showRosterPick(only bool, selected string) {
	if intent := u.main.takeDisplayIntent(); intent != nil && u.main.displaySession(*intent) && intent.Thread.Caller != "/root" {
		u.agents.Selected = intent.Thread.Caller
	}
	if u.main != nil && (u.main.sessionPresentation().DisplayingChild() != only || u.agents.Selected != selected) {
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
