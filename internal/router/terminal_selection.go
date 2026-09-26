package router

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/rivo/uniseg"
)

type terminalSelection struct {
	rect                       terminalRect
	rows                       []string
	startX, startY, endX, endY int
	dragging, moved            bool
	link                       string
	view                       *liveActivityView
	pane                       terminalRect
	questions                  []uint64
	snippets                   []liveActivitySnippet
}

func (s *terminalSelection) bounds(y int) (int, int) {
	ax, ay, bx, by := s.startX, s.startY, s.endX, s.endY
	if ay > by || ay == by && ax > bx {
		ax, ay, bx, by = bx, by, ax, ay
	}
	if y < ay || y > by {
		return 0, 0
	}
	left, right := s.rect.x, s.rect.x+s.rect.w
	if y == ay {
		left = ax
	}
	if y == by {
		right = bx + 1
	}
	// Include the entire grapheme when either endpoint lands on a wide cell.
	column := 0
	for g := uniseg.NewGraphemes(ansi.Strip(s.rows[y])); g.Next(); {
		next := column + g.Width()
		if column < left && left < next {
			left = column
		}
		if column < right && right < next {
			right = next
		}
		column = next
		if column >= right {
			break
		}
	}
	return left, right
}

func (s *terminalSelection) text() string {
	var lines []string
	for y, row := range s.rows {
		left, right := s.bounds(y)
		if right > left {
			lines = append(lines, strings.TrimRight(ansi.Strip(ansi.Cut(row, left, right)), " "))
		}
	}
	return strings.Join(lines, "\n")
}

func (u *terminalUI) selectionAction(action byte) {
	if u.selection == nil {
		return
	}
	switch action {
	case 'r':
		u.main.run = runNone
		u.main.insertDraft("> " + u.selection.text() + "\n\n")
		u.main.run = runNone
		u.focus = 0
	case 'c':
		u.copyText(u.selection.text())
	default:
		return
	}
	u.selection = nil
}

func (u *terminalUI) copyText(text string) {
	u.clipboard = "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	u.main.notice, u.main.noticeAlert = "Copy sent to terminal clipboard", false
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	previous := u.selection
	if s := u.selection; s != nil {
		if s.dragging && (release || button&32 != 0 && button&3 == 0) {
			s.endX = min(max(x, s.rect.x), s.rect.x+s.rect.w-1)
			s.endY = min(max(y, s.rect.y), s.rect.y+s.rect.h-1)
			s.moved = s.moved || s.endX != s.startX || s.endY != s.startY
			if release {
				s.dragging = false
				if !s.moved {
					u.selection = nil
					if s.link != "" {
						u.copyText(s.link)
					} else {
						index := s.startY - s.rect.y
						if index < len(s.questions) && s.questions[index] != 0 {
							if target, ok := s.view.questionRows[s.questions[index]]; ok {
								s.view.offset, s.view.following = target, false
								s.view.flashQuestion = s.questions[index]
								s.view.flashUntil = time.Now().Add(700 * time.Millisecond)
							}
						} else if index < len(s.snippets) && s.snippets[index] != (liveActivitySnippet{}) {
							s.view.toggleSnippet(s.snippets[index])
						}
					}
				}
			}
			return true
		}
		if !s.dragging && button == 0 && !release && y == u.height-1 {
			switch selectionHints.actionAt(x, u.width) {
			case 'r':
				u.selectionAction('r')
			case 'c':
				u.selectionAction('c')
			case 27:
				u.selection = nil
			}
			return true
		}
		if button&64 != 0 || button == 0 && !release {
			u.selection = nil
		}
	}
	if button != 0 || release || u.drag != 0 || u.main == nil || u.main.keybindings {
		return false
	}
	var view *liveActivityView
	var pane terminalRect
	switch {
	case u.layout.codex.contains(x, y):
		view, pane = u.main.view, u.layout.codex
	case u.layout.agents.contains(x, y):
		view, pane = u.agents, u.layout.agents
	default:
		return false
	}
	rect := terminalRect{pane.x + view.feedLeft - 1, pane.y + view.feedTop - 1, view.feedRight - view.feedLeft + 1, view.feedRows}
	rect.w = min(rect.w, pane.x+pane.w-rect.x)
	rect.h = min(rect.h, pane.y+pane.h-rect.y)
	questions, snippets := view.feedQuestions, view.feedSnippets
	if view == u.main.view && !rect.contains(x, y) {
		composer := u.main.composerRect
		composer.x += pane.x
		composer.y += pane.y
		if composer.contains(x, y) {
			rect = composer
			questions, snippets = nil, nil
		}
	}
	if !rect.contains(x, y) || len(u.paintedRows) != u.height {
		return false
	}
	screen := vt.NewEmulator(u.width, u.height)
	defer screen.Close()
	source := u.paintedRows
	if previous != nil && previous.view == view && previous.rect.contains(x, y) {
		source, rect = previous.rows, previous.rect
		questions, snippets = previous.questions, previous.snippets
	}
	for row, line := range source {
		fmt.Fprintf(screen, "\x1b[%d;1H%s", row+1, line)
	}
	link := ""
	if cell := screen.CellAt(x, y); cell != nil {
		link = cell.Link.URL
	}
	if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
		link = parsed.Path
	}
	if view == u.main.view {
		u.focus = 0
	} else {
		u.focus = 2
	}
	u.selection = &terminalSelection{rect: rect, rows: strings.Split(screen.Render(), "\n"), startX: x, startY: y, endX: x, endY: y, dragging: true, link: link, view: view, pane: pane, questions: questions, snippets: snippets}
	return true
}

func (u *terminalUI) paintSelection(rows []string) {
	s := u.selection
	if s == nil {
		return
	}
	if len(s.rows) != len(rows) || u.paintedWidth != u.width || s.pane != u.layout.codex && s.pane != u.layout.agents {
		u.selection = nil
		return
	}
	for y := s.rect.y; y < s.rect.y+s.rect.h; y++ {
		line := ansi.Cut(s.rows[y], s.rect.x, s.rect.x+s.rect.w)
		if left, right := s.bounds(y); s.moved && right > left {
			line = ansi.Cut(s.rows[y], s.rect.x, left) + "\x1b[7m" + ansi.Strip(ansi.Cut(s.rows[y], left, right)) + "\x1b[27m" + ansi.Cut(s.rows[y], right, s.rect.x+s.rect.w)
		}
		rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", s.rect.x+1, line)
	}
	if !s.dragging {
		rows[len(rows)-1] = "\x1b[1G\x1b[0m" + ansi.Truncate(selectionHints.render(), u.width, "")
	}
}
