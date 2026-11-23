package terminalsession

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
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
	SelectionSource        SourceContext
	Width, Height          int
	Now                    time.Time
	Version                string
	FamilyRoot             string // Main of the live family, not necessarily of the attachment.
	LiveHidden, DiffUnseen bool
	Prefix, Playback       bool
	PlaybackBar            string
	FileLinks              map[string]bool
	NoticeEntries          []uint64 // Claimed Main event rows, acknowledged only inside the viewport.
}

type RenderedFrame struct {
	Images                         terminalui.ImageFrame
	ImageDemands                   []terminalui.ImageSource
	FileLinkDemands                []string
	Notices                        NoticeReceipt
	PaintedNoticeEntries           []uint64
	Source                         SourceContext
	Rows                           []string
	Copy                           [][]terminalui.CopySpan
	Demands                        []activitypresentation.OutputDemand
	SelectionRetained, DiffPainted bool
	Prompt                         PromptReceipt
	PromptVisible                  bool
	composer                       composerDocument
}

// SourceContext identifies the selected publication without exposing its model.
func (in FrameInput) SourceContext() SourceContext {
	view := in.Main.session
	return SourceContext{Attachment: view.Version.Attachment, Thread: view.Displayed.ID, Workspace: view.SourceWorkspace()}
}

func (in FrameInput) displayedTranscript() *activityui.View {
	if in.Main.session.Displayed.ID != in.Main.session.Main.ID {
		return in.Agents
	}
	return in.Transcript
}

// EmptyComposer reports whether arrow keys reach the stat-line navigation:
// Main holds no draft and no prompt, menu or overlay takes the arrows.
// Read-only views have no composer, so their arrows are always free.
func (in FrameInput) EmptyComposer() bool {
	m := in.Main
	if m == nil {
		return false
	}
	v := m.session
	if v.ReadOnly != sessionpresentation.Editable {
		return true
	}
	return v.Draft.Draft.Text == "" && v.Questions.Call == nil && !v.Approvals.Open && v.Panel == nil && v.Resume == nil &&
		!m.Menu.Open() && !m.Panel.Open() && !m.Resume.Open() && !m.BTW.Open() && !m.Keybindings
}

// Status derives the second stat line from the same detached publication as the frame.
// Source: apps/mekugi/internal/router/native_shell.go:156:174@9fbf51e1.
func (in FrameInput) Status() ShellStatus {
	if in.Playback {
		return ShellStatus{Playback: true, PlaybackBar: in.PlaybackBar}
	}
	v := in.Main.session
	status := ShellStatus{
		Agent: in.rosterThreadLabel(), MainTab: v.Displayed.ID != v.Main.ID, Scroll: scrollLabel(in.displayedTranscript()), Summary: in.Agents.RosterSummary(),
		DiffUnseen: in.DiffUnseen, Prefix: in.Prefix, LiveCount: in.Live.Live(), Locked: in.Main.InterruptLocked, Empty: in.EmptyComposer(),
		Workspace: v.SourceWorkspace(),
	}
	if in.Shell.DiffOpen && in.Shell.Focus == 1 && in.Shell.Auxiliary != nil {
		summary, state := in.Shell.Auxiliary.Title()
		status.DiffSummary = strings.TrimPrefix(summary, "saved · ")
		status.DiffState = compactDiffState(state)
	}
	if v.Displayed.ID == v.Main.ID {
		status.Title = sessionTitle(v.Info.Title)
	}
	if skills := in.displayedSkills(); len(skills) > 0 {
		status.Skills = activityui.SkillLabel(skills)
	}
	if side := v.Side; side != nil && v.SideThread.ID != "" {
		status.SideLabel, status.SideSelected = side.Label, v.Displayed == v.SideThread
	}
	return status
}

// compactDiffState drops focus and editor labels that the Diff navigator and
// stat-line hints already show.
func compactDiffState(state string) string {
	if state == "" {
		return ""
	}
	const sep = terminalui.Dim + " · " + terminalui.Undim
	var kept []string
	for part := range strings.SplitSeq(state, sep) {
		switch part {
		case "", "files focus", "diff focus", "e editor":
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, sep)
}

// skillsCaller names the displayed agent whose loaded skills the stat line shows.
func (in FrameInput) skillsCaller() string {
	v := in.Main.session
	if v.Displayed.ID != v.Main.ID {
		return v.Displayed.Caller
	}
	return cmp.Or(v.Info.Caller, "/root")
}

func (in FrameInput) displayedSkills() []string {
	v := in.Main.session
	if caller := in.skillsCaller(); v.Displayed.ID != v.Main.ID || caller != "/root" {
		for _, row := range in.Agents.Roster() {
			if row.Agent.Name == caller {
				return row.Agent.Skills
			}
		}
		return nil
	}
	return v.Info.Skills
}

// Render composes terminal-owned surfaces. Output demands must be hydrated and
// rendered again before Rows can be delivered. A prompt receipt is acknowledged
// only after the caller successfully writes these rows through Frame.Write.
// Source: apps/mekugi/internal/router/native_shell.go:55:174 and
// terminal_selection.go:319:338@9fbf51e1.
func (f *Frame) Render(ctx context.Context, in FrameInput) (out RenderedFrame, err error) {
	out.SelectionRetained = true
	view, shell := in.Main.session, in.Shell
	out.Source = in.SourceContext()
	out.Prompt = in.Prompts.Update(view)
	in.Main.PromptRect, in.Main.ContentPainted = Rect{}, false
	in.Main.NoticeDetails, in.Main.NoticeDismiss, in.Main.ProcessCount = Rect{}, Rect{}, Rect{}
	width, height := max(1, in.Width), max(1, in.Height)
	in.Agents.FeedOnly, in.Agents.Focused = true, shell.Focus == 3
	in.Live.Motion.Enabled, in.Live.Motion.Now = true, in.Now
	in.Live.Motion.Canvas = shell.Theme.Canvas()
	if in.Agents.Painter.Colors.HasBackground {
		in.Live.Motion.Canvas.Background = in.Agents.Painter.Colors.Background
	}
	caller := view.Displayed.Caller
	transcript := in.displayedTranscript()
	transcript.ImageDemands, transcript.ImagePlacements = nil, nil
	mainEdit := !in.LiveHidden && (slices.Contains(in.Live.Callers(), caller) || caller == "/root" && len(in.Live.Callers()) > 0)
	in.Agents.Hits = nil
	// The roster sheet needs subagents; it closes when the last one leaves.
	summary := in.Agents.RosterSummary()
	if shell.Focus == 3 && !summary.Children {
		shell.Focus = 0
	}
	plan := shell.Plan(width, height)
	processRect, rosterRect := Rect{}, Rect{}
	var roster []string
	var mainImageDemands []terminalui.ImageSource
	var mainImagePlacements []terminalui.ImagePlacement
	processLimit := 0
	content := ShellContent{}
	if plan.Left.W >= 4 && plan.Left.H >= 1 {
		iw, ih := plan.Layout.Main.W, plan.Layout.Main.H
		// The process list, content sheets and the roster dock as one panel
		// kind: Main gives up the rows under its transcript, which keeps its
		// latest rows.
		panel := in.Dialog != nil
		if panel {
			minimum := in.Dialog.Chrome() + 1
			switch {
			case in.Dialog.ProcessDetail:
				minimum = processDetailMinHeight
			case !in.Dialog.Processes:
				// A content sheet keeps the three body rows it is fitted to.
				minimum = in.Dialog.Chrome() + 3
			}
			processLimit = min(ih, max(minimum, ih/2))
			panelHeight := processLimit
			switch {
			case !in.Dialog.Processes:
				panelHeight = in.Dialog.SheetHeight(iw, processLimit)
			case in.Dialog.ProcessDetail:
				panelHeight = in.Dialog.processDetailHeight(iw, processLimit)
			default:
				panelHeight = min(panelHeight, in.Dialog.Chrome()+len(in.Dialog.Pages)+1)
			}
			ih -= panelHeight
			processRect = Rect{plan.Layout.Main.X, plan.Layout.Main.Y + ih, iw, panelHeight}
		} else if shell.Focus == 3 && iw >= 24 {
			// A blank row above the heading rule; heading and footer frame
			// the agent rows. The heading carries the role legend.
			roster = in.Agents.RosterSheet(iw-4, min(ih-1, max(5, ih/2))-2, in.Now)
			if len(roster) > 0 {
				// The a key names the view it switches to.
				next := "a child"
				if caller := in.Agents.Display.Caller; caller != "" && caller != "/root" {
					next = "a main"
				}
				roster = append(roster, terminalui.Dim+"↑↓ agent · "+next+" · esc close"+terminalui.Undim)
				counts := strings.Join(summary.Counts, terminalui.Dim+" · "+terminalui.Undim)
				legend := in.Agents.RoleLegend(max(0, iw-4-ansi.StringWidth(shell.RosterLabel+" · "+counts)-10))
				roster[0] = terminalui.PanelHeading(shell.Theme.Accent(), shell.RosterLabel, counts, legend, iw-4)
				ih -= len(roster) + 1
				rosterRect = Rect{plan.Layout.Main.X + 2, plan.Layout.Main.Y + ih + 1, iw - 4, len(roster)}
				panel = true
			}
		}
		dockRows := 0
		if !panel && mainEdit && !in.Main.Keybindings && !in.Main.Menu.Open() {
			dockRows = ih
		}
		rosterHits := in.Agents.Hits
		main := in.Main.Render(in.displayedTranscript(), &in.Transcript.Painter, in.Prompts, MainOptions{Now: in.Now, Focused: shell.Focus == 0, Version: in.Version, HideComposer: panel}, iw, ih, dockRows)
		in.Agents.Hits = rosterHits
		if len(main.Demands) != 0 {
			out.Demands = main.Demands
			return out, nil
		}
		out.Notices = main.Notices
		if view.Displayed.ID != view.Main.ID {
			out.Notices.Content = false
		}
		if out.Notices.Content && len(in.NoticeEntries) > 0 {
			for _, span := range transcript.FeedSpans {
				if span.End > transcript.Offset && span.Start < transcript.Offset+transcript.FeedRows && slices.Contains(in.NoticeEntries, span.Seq) {
					out.PaintedNoticeEntries = append(out.PaintedNoticeEntries, span.Seq)
				}
			}
		}
		out.composer = main.composer
		mainImageDemands = main.ImageDemands
		mainImagePlacements = main.ImagePlacements
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
	}
	if plan.Right.W >= 4 && plan.Right.H >= 1 {
		content.Diff, err = in.Diff.Paint(ctx, shell.Auxiliary, plan.Layout.Diff.W, plan.Layout.Diff.H)
		if err != nil {
			content.Diff = []string{shell.AuxiliaryLabel + " unavailable", textsanitize.Safe(err.Error(), false)}
		}
		out.DiffPainted, in.DiffUnseen = true, false
	}
	if plan.Stats > 0 {
		var usage captionUsage
		usage.Tokens, usage.Cost = in.Agents.AgentUsage(in.skillsCaller())
		if view.Displayed.ID == view.Main.ID && summary.Children && summary.Cost != usage.Cost {
			usage.Total = summary.Cost
		}
		content.Stats = []string{
			captionLine(shell.Theme, view.Caption, usage, width),
			shell.StatLine(in.Status(), width, height-1),
		}
	}
	out.Rows = shell.Compose(plan, content)
	shell.Layout.Roster = rosterRect
	for i, line := range roster {
		if y := rosterRect.Y + i; y >= 0 && y < len(out.Rows) {
			out.Rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", rosterRect.X+1, ansi.Truncate(line, rosterRect.W, ""))
		}
	}
	if in.Dialog != nil {
		in.Dialog.FileLinks = in.FileLinks
		in.Dialog.FileLinkDemands = nil
		switch {
		case in.Dialog.Processes:
			in.Dialog.paintProcesses(out.Rows, processRect, processLimit)
		case processRect.H > 0:
			in.Dialog.Paint(out.Rows, processRect)
		default:
			// Without a Main pane the sheet spans the screen above the
			// roster and status rows.
			in.Dialog.Paint(out.Rows, Rect{0, 0, width, max(1, min(height-1, plan.Layout.Main.Y+plan.Layout.Main.H))})
		}
		out.FileLinkDemands = append(out.FileLinkDemands, in.Dialog.FileLinkDemands...)
	}
	out.Copy = make([][]terminalui.CopySpan, len(out.Rows))
	for y := range out.Rows {
		out.Rows[y], out.Copy[y] = terminalui.ExtractCopy(out.Rows[y])
	}
	out.SelectionRetained = f.PaintSelection(in, out.Rows)
	selectionPainted := in.Selection != nil && in.Dialog == nil && out.SelectionRetained
	selected := Rect{}
	if selectionPainted {
		selected = in.Selection.Rect
		if in.SelectionPane == shell.Layout.Main {
			out.Source = in.SelectionSource
		}
	}
	if in.Dialog != nil {
		out.Source = SourceContext{}
	}
	if in.Dialog != nil || selectionPainted {
		out.Notices.Composer, out.Notices.Content = false, false
		out.PaintedNoticeEntries = nil
	}
	// Overlays use cursor-positioned painting. Resolve their final cells only on
	// actually overlapped rows so covered transcript candidates cannot request
	// filesystem work, without stripping OSC 8 links from visible transcript rows.
	if selectionPainted {
		for y := max(0, selected.Y); y < min(len(out.Rows), selected.Y+selected.H); y++ {
			if strings.Contains(out.Rows[y], "\x1b]8;") {
				visible, _ := Screen(width, 1, []string{out.Rows[y]}, -1, -1)
				out.Rows[y] = visible[0]
			}
		}
	} else if in.Dialog != nil && processRect.H == 0 {
		limit := min(len(out.Rows), max(1, min(height-1, plan.Layout.Main.Y+plan.Layout.Main.H)))
		for y := range limit {
			if strings.Contains(out.Rows[y], "\x1b]8;") {
				visible, _ := Screen(width, 1, []string{out.Rows[y]}, -1, -1)
				out.Rows[y] = visible[0]
			}
		}
	}
	for y, row := range out.Rows {
		var demands []string
		out.Rows[y], demands = terminalui.ResolveFileLinks(row, in.FileLinks, shell.Theme.Accent())
		out.FileLinkDemands = append(out.FileLinkDemands, demands...)
	}
	out.PromptVisible = shell.PromptVisible(in.Main.PromptRect, in.Dialog != nil, selected)
	if in.Dialog != nil && in.Selection == nil {
		out.Images = in.Dialog.Images()
		if source := in.Dialog.ImageSource(); source.Path != "" && out.Images.Protocol != terminalui.NoImages && in.Dialog.Selection == nil {
			out.ImageDemands = []terminalui.ImageSource{source}
		}
	} else if in.Dialog == nil && in.Selection == nil && view.Panel == nil && view.Resume == nil {
		out.Images = terminalui.ImageFrame{Protocol: transcript.ImageProtocol, CellWidth: transcript.ImageCellWidth, CellHeight: transcript.ImageCellHeight}
		if !in.Main.Keybindings && !in.Main.Menu.Open() && !mainEdit {
			out.ImageDemands = append(out.ImageDemands, transcript.ImageDemands...)
			for _, p := range transcript.ImagePlacements {
				p.X += plan.Layout.Main.X + transcript.FeedLeft - 1
				p.Y += plan.Layout.Main.Y + transcript.FeedTop - 1
				out.Images.Placements = append(out.Images.Placements, p)
			}
		}
		out.ImageDemands = append(out.ImageDemands, mainImageDemands...)
		for _, p := range mainImagePlacements {
			p.X += plan.Layout.Main.X
			p.Y += plan.Layout.Main.Y
			out.Images.Placements = append(out.Images.Placements, p)
		}
	}
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
