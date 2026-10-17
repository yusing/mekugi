package router

import (
	"cmp"
	"fmt"
	"github.com/yusing/mekugi/libraries/terminalui"
	"net/url"
	"slices"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// terminalSelection binds the terminal's frozen source selection to feature
// targets. Selection mechanics never depend on these live application objects.
type terminalSelection struct {
	*terminalsession.Selection
	link      string
	view      *activityui.View
	pane      terminalsession.Rect
	questions []uint64
	snippets  []activityui.Snippet
	mention   string
	diff      []diffrender.LineSource
	workspace string
}

// selectionKey owns scrolling and action dispatch for both selection surfaces.
// Callers retain navigation dismissal and their surrounding Escape priority.
func (u *terminalUI) selectionKey(key string) bool {
	s := u.selection
	if s == nil {
		return false
	}
	intent, handled := s.Key(key, false)
	if intent != terminalsession.SelectionScroll {
		u.selectionAction(intent)
	}
	return handled
}

func (u *terminalUI) selectionAction(action terminalsession.SelectionIntent) {
	s := u.selection
	if s == nil || s.Dragging() && action != 27 {
		return
	}
	switch action {
	case 'r':
		description, source := s.mentionDescription()
		if u.main.composerReadOnly() {
			u.selectNativePane(0)
		}
		u.main.insertSelection(description, s.Text(), source)
		u.main.refreshPicker()
		u.main.Run = composer.NoRun
		u.Focus = 0
		u.closeOutput()
	case 'c':
		u.copyText(s.Text())
	case 27:
	default:
		return
	}
	u.selection = nil
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	previous := u.selection
	footer := terminalsession.Rect{0, u.height - 1, u.width, 1}
	if s := previous; s != nil {
		if s.Mouse(button, x, y, release) {
			if release && !s.Moved() {
				u.selection = nil
				if s.link != "" {
					if !u.main.openMarkdownFile(u, s.view, s.link) {
						link := s.link
						if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
							link = parsed.Path
						}
						u.copyText(link)
					}
				} else {
					index := s.PressRow() - s.Rect.Y
					if index >= 0 && index < len(s.questions) && s.questions[index] != 0 {
						if s.view == u.main.view && u.openActivityReply(s.questions[index]) {
							return true
						}
						u.openEntry(s.view, s.questions[index])
					} else if index >= 0 && index < len(s.snippets) && s.snippets[index] != (activityui.Snippet{}) {
						snippet := s.snippets[index]
						if snippet.Block == activityui.EditNavigationSnippet {
							u.openActivityEdit(s.view, snippet.Run, snippet.Path)
						} else if !u.openOutput(s.view, snippet) {
							u.openEntry(s.view, snippet.Run)
						}
					}
				}
			}
			return true
		}
		if !s.Dragging() && button == 0 && !release && footer.Contains(x, y) {
			u.selectionAction(terminalsession.SelectionIntent(terminalsession.SelectionHints.ActionAt(x, u.width)))
			return true
		}
		if button&64 != 0 || button == 0 && !release {
			u.selection = nil
		}
	}
	if button != 0 || release || u.Dragging() || u.main == nil || u.main.mainView.Keybindings {
		return false
	}
	var view *activityui.View
	var pane terminalsession.Rect
	mention := "message"
	switch {
	case u.Layout.Main.Contains(x, y):
		view, pane = u.main.transcriptView(), u.Layout.Main
	case u.Layout.Diff.Contains(x, y):
		// The press still reaches the diff pane, so clicks keep their meaning.
		if previous != nil && previous.diff != nil && previous.ScreenRows() != 0 && previous.pane == u.Layout.Diff && previous.Rect.Contains(x, y) {
			u.selection = &terminalSelection{Selection: previous.Restart(x, y), pane: previous.pane, diff: previous.diff, workspace: previous.workspace}
		} else {
			u.selection = u.diffSelection(x, y)
		}
		return false
	default:
		return false
	}
	rect := terminalsession.Rect{pane.X + view.FeedLeft - 1, pane.Y + view.FeedTop - 1, view.FeedRight - view.FeedLeft + 1, view.FeedRows}
	rect.W = min(rect.W, pane.X+pane.W-rect.X)
	rect.H = min(rect.H, pane.Y+pane.H-rect.Y)
	questions, snippets := view.FeedQuestions, view.FeedSnippets
	if u.main.mainView.Side.Open() {
		btw := u.main.mainView.Side.Rect
		btw.X += pane.X
		btw.Y += pane.Y
		if btw.Contains(x, y) {
			rect, mention, questions, snippets = btw, "side answer", nil, nil
		}
	}
	if !rect.Contains(x, y) {
		composer := u.main.mainView.ComposerRect
		composer.X += pane.X
		composer.Y += pane.Y
		if composer.Contains(x, y) {
			rect, mention = composer, "text"
			questions, snippets = nil, nil
		}
	}
	if !u.main.composerReadOnly() && u.main.mainView.Panel.Open() {
		rect, mention = u.main.mainView.Panel.Rect, "status"
		rect.X += pane.X
		rect.Y += pane.Y
		questions, snippets = nil, nil
	}
	if !rect.Contains(x, y) || len(u.frame.Rows) != u.height {
		return false
	}
	source := u.frame.Rows
	copySource := u.frame.Copy
	if previous != nil && previous.view == view && previous.Rect.Contains(x, y) {
		rect = previous.Rect
		source, copySource = slices.Clone(source), slices.Clone(copySource)
		for row := rect.Y; row < rect.Y+rect.H; row++ {
			at := previous.DocumentY(row)
			source[row] = previous.Rows[at]
			if row < len(copySource) && at < len(previous.CopySource) {
				copySource[row] = previous.CopySource[at]
			}
		}
		questions, snippets, mention = previous.questions, previous.snippets, previous.mention
	}
	rows, link := terminalsession.Screen(u.width, u.height, source, x, y)
	u.Focus = 0
	u.selection = &terminalSelection{Selection: terminalsession.NewSelection(terminalsession.Surface{Rect: rect, Rows: rows, CopySource: copySource}, x, y), link: link, view: view, pane: pane, questions: questions, snippets: snippets, mention: mention}
	if previous != nil && previous.ScreenRows() != 0 && previous.view == view && previous.Rect == rect {
		next := *previous
		next.Selection, next.link = previous.Restart(x, y), link
		u.selection = &next
	} else {
		u.selectionDocument(u.selection)
	}
	return true
}

func (u *terminalUI) selectionDocument(s *terminalSelection) {
	switch s.mention {
	case "message", "activity":
		if s.view.FeedLines > s.Rect.H {
			feed := renderActivityFeed(u.main.activitySource(s.view), s.view, s.Rect.W, s.view.FeedLines)
			top, fixed := s.view.Offset, 0
			if !s.view.Conversation && top+1 < len(feed.Heads) && feed.Heads[top] != top && feed.Heads[top+1] == feed.Heads[top] {
				feed.Lines = append([]string{feed.Lines[feed.Heads[top]]}, feed.Lines...)
				feed.Questions = append([]uint64{0}, feed.Questions...)
				feed.Snippets = append([]activityui.Snippet{{}}, feed.Snippets...)
				top, fixed = top+1, 1
			}
			s.Document(feed.Lines, top, fixed)
			questions, snippets := s.questions, s.snippets
			s.questions = append(slices.Clone(feed.Questions), make([]uint64, max(0, len(s.Rows)-s.Rect.Y-len(feed.Questions)))...)
			s.snippets = append(slices.Clone(feed.Snippets), make([]activityui.Snippet, max(0, len(s.Rows)-s.Rect.Y-len(feed.Snippets)))...)
			for row := 0; row < s.Rect.H; row++ {
				at := s.DocumentY(s.Rect.Y+row) - s.Rect.Y
				if row < len(questions) {
					s.questions[at] = questions[row]
				}
				if row < len(snippets) {
					s.snippets[at] = snippets[row]
				}
			}
		}
	case "side answer":
		b := &u.main.mainView.Side
		var lines []string
		fixed := s.Rect.H - min(b.Rows, len(b.CopyAnswer))
		for y := s.Rect.Y; y < s.Rect.Y+fixed; y++ {
			lines = append(lines, ansi.Cut(s.Rows[y], s.Rect.X, s.Rect.X+s.Rect.W))
		}
		lines = append(lines, b.CopyAnswer...)
		s.Document(lines, max(0, len(b.CopyAnswer)-b.Rows-b.Scroll), fixed)
	}
}

// diffSelection starts a selection right of the file navigator and its
// divider; each row's text starts after its gutter and line number.
func (u *terminalUI) diffSelection(x, y int) *terminalSelection {
	c, ok := u.Auxiliary.(terminalui.PaneSelection)
	pane := u.Layout.Diff
	if !ok || len(u.frame.Rows) != u.height || !pane.Contains(x, y) {
		return nil
	}
	source := c.Selection(x-pane.X, y-pane.Y)
	rect := terminalsession.Rect{pane.X + source.X, pane.Y + source.Y, min(pane.W-source.X, source.Width), min(pane.H-source.Y, len(source.Painted))}
	if !rect.Contains(x, y) {
		return nil
	}
	rows, _ := terminalsession.Screen(u.width, u.height, u.frame.Rows, x, y)
	contentLeft := make([]int, rect.Y+rect.H)
	sources := make([]diffrender.LineSource, rect.Y+rect.H)
	for row, line := range source.Painted[:rect.H] {
		contentLeft[rect.Y+row] = rect.X + line.Content
		sources[rect.Y+row] = line
	}
	s := &terminalSelection{Selection: terminalsession.NewSelection(terminalsession.Surface{Rect: rect, Rows: rows, ContentLeft: contentLeft}, x, y), pane: pane, diff: sources, workspace: source.Workspace}
	if len(source.Document)-source.Fixed > rect.H {
		s.Document(source.Document, source.Offset, source.Fixed)
		s.ContentLeft = make([]int, len(s.Rows))
		s.diff = make([]diffrender.LineSource, len(s.Rows))
		for i, line := range source.Sources {
			if rect.Y+i >= len(s.Rows) {
				break
			}
			s.ContentLeft[rect.Y+i] = rect.X + line.Content
			s.diff[rect.Y+i] = line
		}
	}
	return s
}

// mentionDescription names the selection concisely: its pane, or for the
// saved diff, the change IDs and gutter lines the selected rows show.
func (s *terminalSelection) mentionDescription() (string, string) {
	if s.diff == nil {
		return s.mention, ""
	}
	_, first, _, last := s.Ordered()
	var changes, paths []string
	// Gutter lines of changed rows, used only for one change. New-file
	// coordinates win; a deletion-only selection names old-file lines.
	low, high, oldLow, oldHigh := 0, 0, 0, 0
	for y := first; y <= last && y < len(s.diff); y++ {
		if left, right := s.Bounds(y); right <= left {
			continue
		}
		source := s.diff[y]
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
		path = pathdisplay.ForWorkspace(s.workspace, paths[0])
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
