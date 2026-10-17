package terminalsession

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

// FrameInput borrows the attached terminal objects for one render pass. It has
// no application pointer, source reducer, output resolver or backend capability.
type FrameInput struct {
	Main                   *Main
	Prompts                *Prompts
	Transcript, Agents     *activityui.View
	Shell                  *Shell
	Dialog                 *ContentDialog
	Diff                   *DiffScreen
	Live                   *previewview.PreviewPane
	Selection              *Selection
	SelectionPane          Rect
	Width, Height          int
	Now                    time.Time
	Version                string
	LiveHidden, DiffUnseen bool
	Prefix, Playback       bool
	PlaybackBar            string
}

type RenderedFrame struct {
	Notices                        NoticeReceipt
	Rows                           []string
	Copy                           [][]terminalui.CopySpan
	Demands                        []activitypresentation.OutputDemand
	SelectionRetained, DiffPainted bool
	Prompt                         PromptReceipt
	PromptVisible                  bool
}

func (in FrameInput) displayedTranscript() *activityui.View {
	if in.Main.session.Displayed.ID != in.Main.session.Main.ID {
		return in.Agents
	}
	return in.Transcript
}

// Status derives terminal labels from the same detached publication as the frame.
// Source: apps/mekugi/internal/router/native_shell.go:156:174@9fbf51e1.
func (in FrameInput) Status() ShellStatus {
	if in.Playback {
		return ShellStatus{Playback: true, PlaybackBar: in.PlaybackBar}
	}
	responding, _ := in.Agents.StatusCounts(in.Agents.Roster())
	status := ShellStatus{
		Child: in.Main.session.Displayed.ID != in.Main.session.Main.ID, Responding: responding,
		DiffUnseen: in.DiffUnseen, Prefix: in.Prefix, LiveCount: in.Live.Live(), Locked: in.Main.InterruptLocked,
	}
	if in.Shell.Focus == 0 && !in.Prefix {
		status.Expansion = in.displayedTranscript().Expansion
	}
	return status
}

// Render composes terminal-owned surfaces. Output demands must be hydrated and
// rendered again before Rows can be delivered. A prompt receipt is acknowledged
// only after the caller successfully writes these rows through Frame.Write.
// Source: apps/mekugi/internal/router/native_shell.go:55:174 and
// terminal_selection.go:319:338@9fbf51e1.
func (f *Frame) Render(ctx context.Context, in FrameInput) (out RenderedFrame, err error) {
	out.SelectionRetained = true
	view, shell := in.Main.session, in.Shell
	in.Agents.HistoryHint = in.Main.historyHint()
	out.Prompt = in.Prompts.Update(view)
	in.Main.PromptRect, in.Main.ContentPainted = Rect{}, false
	in.Main.NoticeDetails, in.Main.NoticeDismiss = Rect{}, Rect{}
	width, height := max(1, in.Width), max(1, in.Height)
	in.Agents.FeedOnly, in.Agents.Focused = true, shell.Focus == 3
	in.Live.Motion.Enabled, in.Live.Motion.Now = true, in.Now
	in.Live.Motion.Canvas = shell.Theme.Canvas()
	if in.Agents.Painter.Colors.HasBackground {
		in.Live.Motion.Canvas.Background = in.Agents.Painter.Colors.Background
	}
	caller := view.Displayed.Caller
	mainEdit := !in.LiveHidden && slices.Contains(in.Live.Callers(), caller)
	in.Agents.LivePreviews = nil
	var roster []string
	if shell.RosterLabel != "" {
		roster = in.Agents.NativeRoster(width, shell.Panes.RosterLimit(height), in.Now, shell.Focus == 3)
	} else {
		in.Agents.Hits = nil
	}
	plan := shell.Plan(width, height, len(roster))
	content := ShellContent{Roster: roster, MainName: "Main"}
	if plan.Left.W >= 4 && plan.Left.H >= 1 {
		iw, ih := plan.Layout.Main.W, plan.Layout.Main.H
		dockRows := 0
		if mainEdit && !in.Main.Keybindings && !in.Main.Menu.Open() {
			dockRows = ih
		}
		rosterHits := in.Agents.Hits
		main := in.Main.Render(in.displayedTranscript(), &in.Transcript.Painter, in.Prompts, MainOptions{Now: in.Now, Focused: shell.Focus == 0, Version: in.Version}, iw, ih, dockRows)
		in.Agents.Hits = rosterHits
		if len(main.Demands) != 0 {
			out.Demands = main.Demands
			return out, nil
		}
		out.Notices = main.Notices
		content.Main, content.Dock = main.Rows, main.Dock
		dockAt, dockRows := content.Dock.Y, content.Dock.H
		if dockRows > 0 && dockAt+dockRows <= len(content.Main) {
			dock, err := in.Live.RenderBatch(ctx, caller, view.Info.Workspace, shell.Theme, iw, dockRows, 15)
			if err != nil {
				return out, err
			}
			for i := range dockRows {
				content.Main[dockAt+i] = ""
				if i < len(dock) {
					content.Main[dockAt+i] = dock[i]
				}
			}
		}
		if plan.Framed {
			content.MainDetail = in.Main.titleDetail(shell.Focus == 0)
			if set := view.Info.Skills; set != nil {
				content.MainSkills = activityui.SkillLabel(set)
				if in.Transcript.SkillsHover == "Main" {
					content.MainDetail = strings.TrimSuffix(content.MainDetail, content.MainSkills) + activityui.Underline(content.MainSkills)
				}
			}
			content.MainRight = in.Main.HeaderRight(in.Transcript, plan.Left.W, shell.Focus == 0)
			if view.Displayed.ID != view.Main.ID {
				content.MainName, content.MainDetail = "Thread", in.Agents.Painter.Agent(view.Displayed.Caller)
				content.MainSkills = ""
				content.MainRight = strings.TrimSpace(scrollLabel(in.Agents) + " " + in.Agents.HistoryHint)
			}
		}
	}
	if plan.Right.W >= 4 && plan.Right.H >= 1 {
		content.Diff, err = in.Diff.Paint(ctx, shell.Auxiliary, plan.Layout.Diff.W, plan.Layout.Diff.H)
		if err != nil {
			content.Diff = []string{shell.AuxiliaryLabel + " unavailable", textsanitize.Safe(err.Error(), false)}
		}
		content.DiffSummary, content.DiffState = shell.Auxiliary.Title()
		out.DiffPainted, in.DiffUnseen = true, false
	}
	content.Status = in.Status()
	out.Rows = shell.Compose(plan, content)
	if in.Dialog != nil {
		in.Dialog.Paint(out.Rows[:height-1], width, height-1)
	}
	out.Copy = make([][]terminalui.CopySpan, len(out.Rows))
	for y := range out.Rows {
		out.Rows[y], out.Copy[y] = terminalui.ExtractCopy(out.Rows[y])
	}
	out.SelectionRetained = f.PaintSelection(in, out.Rows)
	selected := Rect{}
	if in.Selection != nil && out.SelectionRetained {
		selected = in.Selection.Rect
	}
	if in.Dialog != nil || in.Selection != nil && out.SelectionRetained {
		out.Notices.Composer, out.Notices.Content = false, false
	}
	out.PromptVisible = shell.PromptVisible(in.Main.PromptRect, in.Dialog != nil, selected)
	return out, nil
}

// PaintSelection overlays a frozen source document and validates its captured geometry.
func (f *Frame) PaintSelection(in FrameInput, rows []string) bool {
	s := in.Selection
	if s == nil || in.Dialog != nil {
		return true
	}
	if cmp.Or(s.ScreenRows(), len(s.Rows)) != len(rows) || f.Width != in.Width || in.SelectionPane != in.Shell.Layout.Main && in.SelectionPane != in.Shell.Layout.Diff {
		return false
	}
	for y := s.Rect.Y; y < s.Rect.Y+s.Rect.H; y++ {
		line := ansi.Cut(s.Row(s.DocumentY(y), in.Transcript.Painter.Theme), s.Rect.X, s.Rect.X+s.Rect.W)
		line += strings.Repeat(" ", max(0, s.Rect.W-ansi.StringWidth(line)))
		rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", s.Rect.X+1, line)
	}
	if !s.Dragging() {
		rows[len(rows)-1] = "\x1b[1G\x1b[0m" + ansi.Truncate(SelectionHints.Render(), in.Width, "")
	}
	return true
}
