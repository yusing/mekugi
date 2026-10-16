// Source: apps/mekugi/internal/router/app_server_composer_history.go:27:110@[e9da429a] local edit and input history.
package composer

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// Service creates independent attached-session editors without a global store.
type Service struct{}

func (Service) NewEditor() Editor { return Editor{} }

type Run uint8

const (
	NoRun Run = iota
	InsertRun
	BackspaceRun
	DeleteRun
)

type Undo struct {
	Draft
	HistoryBack  int
	HistoryDraft Draft
}

// State parks one editor while retaining the session's shared input history.
type State struct {
	Snapshot               Undo
	UndoDrafts, RedoDrafts []Undo
	Run                    Run
}

type Editor struct {
	Draft
	UndoDrafts, RedoDrafts []Undo
	InputHistory           []Draft
	HistoryDraft           Draft
	HistoryBack            int
	Run                    Run
}

func (e *Editor) Remember(draft Draft) {
	if draft.Text == "" {
		return
	}
	e.InputHistory = append(e.InputHistory, draft.Clone())
	if len(e.InputHistory) > 100 {
		e.InputHistory = slices.Clone(e.InputHistory[len(e.InputHistory)-100:])
	}
}

func (e *Editor) Recall(backward bool) bool {
	next := e.HistoryBack - 1
	if backward {
		next = e.HistoryBack + 1
	}
	if next < 0 || next > len(e.InputHistory) {
		return false
	}
	if e.HistoryBack == 0 {
		e.HistoryDraft = e.Draft.Editable()
	}
	e.Record()
	e.HistoryBack = next
	snapshot := e.HistoryDraft
	if next > 0 {
		snapshot = e.InputHistory[len(e.InputHistory)-next]
	}
	e.Draft = snapshot.Editable()
	return true
}

func (e *Editor) Snapshot() Undo {
	return Undo{e.Draft.Editable(), e.HistoryBack, e.HistoryDraft.Clone()}
}
func (e *Editor) Record() {
	e.UndoDrafts = append(e.UndoDrafts, e.Snapshot())
	if len(e.UndoDrafts) > 100 {
		e.UndoDrafts = slices.Clone(e.UndoDrafts[len(e.UndoDrafts)-100:])
	}
	e.RedoDrafts = nil
	e.Run = NoRun
}
func (e *Editor) Undo(redo bool) {
	from, to := &e.UndoDrafts, &e.RedoDrafts
	if redo {
		from, to = to, from
	}
	e.Run = NoRun
	if len(*from) == 0 {
		return
	}
	*to = append(*to, e.Snapshot())
	snapshot := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	e.HistoryBack, e.HistoryDraft = snapshot.HistoryBack, snapshot.HistoryDraft.Clone()
	e.Draft = snapshot.Draft.Editable()
}

func (e *Editor) Insert(text string) {
	at := e.Cursor()
	wordStart := false
	if at > 0 && text != "" {
		before, _ := utf8.DecodeLastRuneInString(e.Text[:at])
		first, _ := utf8.DecodeRuneInString(text)
		wordStart = unicode.IsSpace(before) && !unicode.IsSpace(first)
	}
	if e.Run != InsertRun || wordStart {
		e.Record()
		e.Run = InsertRun
	}
	e.InsertText(text)
}

func (e *Editor) Delete(start, end int, run Run) {
	if start == end {
		e.Run = NoRun
		return
	}
	if run == NoRun || e.Run != run {
		e.Record()
	}
	e.Run = run
	e.Remove(start, end)
}

func (e *Editor) Take() Draft {
	draft := e.Draft.Editable()
	draft.CursorBack = 0
	e.Draft = Draft{}
	e.HistoryBack, e.HistoryDraft = 0, Draft{}
	e.UndoDrafts, e.RedoDrafts = nil, nil
	e.Run = NoRun
	return draft
}
func (e *Editor) Restore(parts ...Draft) {
	e.UndoDrafts, e.RedoDrafts = nil, nil
	e.Run = NoRun
	e.Draft = Join(append(slices.Clone(parts), e.Draft.Editable())...).Editable()
}
func (e *Editor) Park() State { return State{e.Snapshot(), e.UndoDrafts, e.RedoDrafts, e.Run} }
func (e *Editor) Resume(s State) {
	e.Draft = s.Snapshot.Draft.Editable()
	e.HistoryBack, e.HistoryDraft = s.Snapshot.HistoryBack, s.Snapshot.HistoryDraft.Clone()
	e.UndoDrafts, e.RedoDrafts, e.Run = s.UndoDrafts, s.RedoDrafts, s.Run
}

func (e *Editor) InsertImage(path string) {
	e.Run = NoRun
	at := e.Cursor()
	label := fmt.Sprintf("[Image %d]", len(e.Images)+1)
	e.Insert(label)
	e.Images = append(e.Images, Token{Start: at, End: at + len(label), Path: path})
	e.RenumberImages()
	e.Run = NoRun
}

func (e *Editor) InsertSelection(description, text, source string, pending []Draft) error {
	if text == "" {
		return nil
	}
	if len(text) > attachments.Budget/2 {
		return fmt.Errorf("Selection exceeds the %d KiB mention limit; copy it instead", attachments.Budget/2>>10)
	}
	label := SelectionPrefix + description + "]"
	used := func(label string) bool {
		return strings.Contains(e.Text, label) || slices.ContainsFunc(pending, func(d Draft) bool { return strings.Contains(d.Text, label) })
	}
	for n := 2; used(label); n++ {
		label = fmt.Sprintf("%s%s %d]", SelectionPrefix, description, n)
	}
	e.Run = NoRun
	e.Insert(label)
	end := e.Cursor()
	e.Selections = append(e.Selections, Token{Start: end - len(label), End: end, Text: text, Source: source})
	slices.SortFunc(e.Selections, func(a, b Token) int { return a.Start - b.Start })
	e.InsertText(" ")
	return nil
}
