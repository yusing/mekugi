package terminalsession

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// SourceSelection retains frozen terminal content and its original pointer targets.
// It has no source reducer, output handle or application-session pointer.
// Source: apps/mekugi/internal/router/terminal_selection.go:19:317@3e8d2662.
type SourceSelection struct {
	*Selection
	Attachment   uint64
	Thread, Link string
	View         *activityui.View
	Pane         Rect
	Questions    []uint64
	Snippets     []activityui.Snippet
	Mention      string
	Diff         []diffrender.LineSource
	Workspace    string
}

// SelectionTarget identifies an original press, independently of streaming rows.
type SelectionTarget struct {
	Version                 sessionpresentation.Version
	Thread, Workspace, Link string
	View                    *activityui.View
	Entry                   uint64
	Snippet                 activityui.Snippet
}

type SelectionInput struct {
	Selection *SourceSelection
	Handled   bool
	Action    SelectionIntent
	Target    SelectionTarget
	Demands   []activitypresentation.OutputDemand
}

// SelectionMouse routes gestures over the frozen document. A capture with output
// demands must be hydrated and retried before its selection is installed.
func (f *Frame) SelectionMouse(in FrameInput, previous *SourceSelection, mouse Mouse) (out SelectionInput) {
	button, x, y, release := mouse.Button, mouse.X, mouse.Y, mouse.Release
	out.Selection = previous
	footer := Rect{0, in.Height - 1, in.Width, 1}
	if s := previous; s != nil {
		if s.Mouse(button, x, y, release) {
			out.Handled = true
			if release && !s.Moved() {
				out.Selection = nil
				version := in.Main.session.Version
				version.Attachment = s.Attachment
				out.Target = SelectionTarget{Version: version, Thread: s.Thread, Workspace: s.Workspace, View: s.View, Link: s.Link}
				index := s.PressRow() - s.Rect.Y
				if index >= 0 && index < len(s.Questions) && s.Questions[index] != 0 {
					out.Target.Entry = s.Questions[index]
				} else if index >= 0 && index < len(s.Snippets) {
					out.Target.Snippet = s.Snippets[index]
				}
			}
			return out
		}
		if !s.Dragging() && button == 0 && !release && footer.Contains(x, y) {
			out.Action = SelectionIntent(SelectionHints.ActionAt(x, in.Width))
			out.Handled = true
			return out
		}
		if button&64 != 0 || button == 0 && !release {
			out.Selection = nil
		}
	}
	if button != 0 || release || in.Shell.Dragging() || in.Main == nil || in.Main.Keybindings {
		return out
	}
	var view *activityui.View
	var pane Rect
	mention := "message"
	switch {
	case in.Shell.Layout.Main.Contains(x, y):
		view, pane = in.displayedTranscript(), in.Shell.Layout.Main
	case in.Shell.Layout.Diff.Contains(x, y):
		// The press still reaches the diff pane, so clicks keep their meaning.
		if previous != nil && previous.Diff != nil && previous.ScreenRows() != 0 && previous.Pane == in.Shell.Layout.Diff && previous.Rect.Contains(x, y) {
			out.Selection = &SourceSelection{Selection: previous.Restart(x, y), Attachment: previous.Attachment, Thread: previous.Thread, Pane: previous.Pane, Diff: previous.Diff, Workspace: previous.Workspace}
		} else {
			out.Selection = f.DiffSelection(in, x, y)
		}
		return out
	default:
		return out
	}
	rect := Rect{pane.X + view.FeedLeft - 1, pane.Y + view.FeedTop - 1, view.FeedRight - view.FeedLeft + 1, view.FeedRows}
	rect.W = min(rect.W, pane.X+pane.W-rect.X)
	rect.H = min(rect.H, pane.Y+pane.H-rect.Y)
	questions, snippets := view.FeedQuestions, view.FeedSnippets
	if in.Main.Side.Open() {
		btw := in.Main.Side.Rect
		btw.X += pane.X
		btw.Y += pane.Y
		if btw.Contains(x, y) {
			rect, mention, questions, snippets = btw, "side answer", nil, nil
		}
	}
	if !rect.Contains(x, y) {
		composer := in.Main.ComposerRect
		composer.X += pane.X
		composer.Y += pane.Y
		if composer.Contains(x, y) {
			rect, mention = composer, "text"
			questions, snippets = nil, nil
		}
	}
	if in.Main.session.ReadOnly == sessionpresentation.Editable && in.Main.Panel.Open() {
		rect, mention = in.Main.Panel.Rect, "status"
		rect.X += pane.X
		rect.Y += pane.Y
		questions, snippets = nil, nil
	}
	if !rect.Contains(x, y) || len(f.Rows) != in.Height {
		return out
	}
	source := f.Rows
	copySource := f.Copy
	sourceContext := f.Source
	if previous != nil && previous.View == view && previous.Rect.Contains(x, y) {
		sourceContext = SourceContext{Attachment: previous.Attachment, Thread: previous.Thread, Workspace: previous.Workspace}
		rect = previous.Rect
		source, copySource = slices.Clone(source), slices.Clone(copySource)
		for row := rect.Y; row < rect.Y+rect.H; row++ {
			at := previous.DocumentY(row)
			source[row] = previous.Rows[at]
			if row < len(copySource) && at < len(previous.CopySource) {
				copySource[row] = previous.CopySource[at]
			}
		}
		questions, snippets, mention = previous.Questions, previous.Snippets, previous.Mention
	}
	rows, link := Screen(in.Width, in.Height, source, x, y)
	in.Shell.Focus = 0
	out.Selection = &SourceSelection{Selection: NewSelection(Surface{Rect: rect, Rows: rows, CopySource: copySource}, x, y), Link: link, View: view, Pane: pane, Attachment: sourceContext.Attachment, Thread: sourceContext.Thread, Workspace: sourceContext.Workspace, Questions: questions, Snippets: snippets, Mention: mention}
	if previous != nil && previous.ScreenRows() != 0 && previous.View == view && previous.Rect == rect {
		next := *previous
		next.Selection, next.Link = previous.Restart(x, y), link
		out.Selection = &next
	} else {
		out.Demands = out.Selection.captureDocument(in.Main)
	}
	out.Handled = true
	return out
}

func (s *SourceSelection) captureDocument(main *Main) []activitypresentation.OutputDemand {
	switch s.Mention {
	case "message", "activity":
		if s.View.FeedLines > s.Rect.H {
			feed := s.View.RenderFeed(s.Rect.W, s.View.FeedLines)
			if demands := s.View.OutputDemands(); len(demands) > 0 {
				return demands
			}
			top, fixed := s.View.Offset, 0
			if !s.View.Conversation && top+1 < len(feed.Heads) && feed.Heads[top] != top && feed.Heads[top+1] == feed.Heads[top] {
				feed.Lines = append([]string{feed.Lines[feed.Heads[top]]}, feed.Lines...)
				feed.Questions = append([]uint64{0}, feed.Questions...)
				feed.Snippets = append([]activityui.Snippet{{}}, feed.Snippets...)
				top, fixed = top+1, 1
			}
			s.Document(feed.Lines, top, fixed)
			questions, snippets := s.Questions, s.Snippets
			s.Questions = append(slices.Clone(feed.Questions), make([]uint64, max(0, len(s.Rows)-s.Rect.Y-len(feed.Questions)))...)
			s.Snippets = append(slices.Clone(feed.Snippets), make([]activityui.Snippet, max(0, len(s.Rows)-s.Rect.Y-len(feed.Snippets)))...)
			for row := 0; row < s.Rect.H; row++ {
				at := s.DocumentY(s.Rect.Y+row) - s.Rect.Y
				if row < len(questions) {
					s.Questions[at] = questions[row]
				}
				if row < len(snippets) {
					s.Snippets[at] = snippets[row]
				}
			}
		}
	case "side answer":
		b := &main.Side
		var lines []string
		fixed := s.Rect.H - min(b.Rows, len(b.CopyAnswer))
		for y := s.Rect.Y; y < s.Rect.Y+fixed; y++ {
			lines = append(lines, ansi.Cut(s.Rows[y], s.Rect.X, s.Rect.X+s.Rect.W))
		}
		lines = append(lines, b.CopyAnswer...)
		s.Document(lines, max(0, len(b.CopyAnswer)-b.Rows-b.Scroll), fixed)
	}
	return nil
}

// DiffSelection starts a selection right of the file navigator and its
// divider; each row's text starts after its gutter and line number.
func (f *Frame) DiffSelection(in FrameInput, x, y int) *SourceSelection {
	c, ok := in.Shell.Auxiliary.(terminalui.PaneSelection)
	pane := in.Shell.Layout.Diff
	if !ok || len(f.Rows) != in.Height || !pane.Contains(x, y) {
		return nil
	}
	source := c.Selection(x-pane.X, y-pane.Y)
	rect := Rect{pane.X + source.X, pane.Y + source.Y, min(pane.W-source.X, source.Width), min(pane.H-source.Y, len(source.Painted))}
	if !rect.Contains(x, y) {
		return nil
	}
	rows, _ := Screen(in.Width, in.Height, f.Rows, x, y)
	contentLeft := make([]int, rect.Y+rect.H)
	sources := make([]diffrender.LineSource, rect.Y+rect.H)
	for row, line := range source.Painted[:rect.H] {
		contentLeft[rect.Y+row] = rect.X + line.Content
		sources[rect.Y+row] = line
	}
	s := &SourceSelection{Selection: NewSelection(Surface{Rect: rect, Rows: rows, ContentLeft: contentLeft}, x, y), Pane: pane, Diff: sources, Workspace: source.Workspace}
	if len(source.Document)-source.Fixed > rect.H {
		s.Document(source.Document, source.Offset, source.Fixed)
		s.ContentLeft = make([]int, len(s.Rows))
		s.Diff = make([]diffrender.LineSource, len(s.Rows))
		for i, line := range source.Sources {
			if rect.Y+i >= len(s.Rows) {
				break
			}
			s.ContentLeft[rect.Y+i] = rect.X + line.Content
			s.Diff[rect.Y+i] = line
		}
	}
	return s
}

// MentionDescription names the selection concisely: its pane, or for the
// saved diff, the change IDs and gutter lines the selected rows show.
func (s *SourceSelection) MentionDescription() (string, string) {
	if s.Diff == nil {
		return s.Mention, ""
	}
	_, first, _, last := s.Ordered()
	var changes, paths []string
	// Gutter lines of changed rows, used only for one change. New-file
	// coordinates win; a deletion-only selection names old-file lines.
	low, high, oldLow, oldHigh := 0, 0, 0, 0
	for y := first; y <= last && y < len(s.Diff); y++ {
		if left, right := s.Bounds(y); right <= left {
			continue
		}
		source := s.Diff[y]
		if source.Path != "" && !slices.Contains(paths, source.Path) {
			paths = append(paths, source.Path)
		}
		if source.Change == "" {
			continue
		}
		if !slices.Contains(changes, source.Change) {
			changes = append(changes, source.Change)
		}
		switch {
		case source.Line > 0 && source.Deleted:
			oldLow, oldHigh = cmp.Or(min(oldLow, source.Line), source.Line), max(oldHigh, source.Line)
		case source.Line > 0:
			low, high = cmp.Or(min(low, source.Line), source.Line), max(high, source.Line)
		}
	}
	if high == 0 {
		low, high = oldLow, oldHigh
	}
	path := ""
	if len(paths) == 1 {
		path = pathdisplay.ForWorkspace(s.Workspace, paths[0])
	}
	switch {
	case len(changes) == 0:
		return "diff", path
	case len(changes) > 1:
		return fmt.Sprintf("diff hunks @%s +%d", changes[0], len(changes)-1), path
	case high == 0:
		return "diff hunk @" + changes[0], path
	case low == high:
		return fmt.Sprintf("diff hunk @%s:%d", changes[0], low), path
	}
	return fmt.Sprintf("diff hunk @%s:%d-%d", changes[0], low, high), path
}
