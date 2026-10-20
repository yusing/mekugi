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

// HistoryEntry keeps restoration provenance outside editable draft state.
type HistoryEntry struct {
	Draft
	Source   string
	Restored bool
}

type Editor struct {
	Draft
	revision               uint64
	UndoDrafts, RedoDrafts []Undo
	InputHistory           []HistoryEntry
	HistoryDraft           Draft
	HistoryBack            int
	Run                    Run
}

func (e *Editor) Revision() uint64 { return e.revision }

// Changed marks adapter-owned binding changes in the same serialized owner.
// Revisions never enter undo or parked state.
func (e *Editor) Changed() { e.revision++ }

func (e *Editor) Replace(draft Draft) {
	e.Draft = draft.Editable()
	e.Changed()
}

func (e *Editor) InsertText(text string) {
	if text != "" {
		e.Draft.InsertText(text)
		e.Changed()
	}
}

func (e *Editor) Remove(start, end int) {
	if start != end {
		e.Draft.Remove(start, end)
		e.Changed()
	}
}

func (e *Editor) Remember(draft Draft) {
	if draft.Text == "" {
		return
	}
	e.InputHistory = append(e.InputHistory, HistoryEntry{Draft: draft.Clone()})
	if len(e.InputHistory) > 100 {
		e.InputHistory = slices.Clone(e.InputHistory[len(e.InputHistory)-100:])
	}
}

// RestoreHistory reconciles one ordered occurrence with shared or live history.
// Matching is source-scoped and consumes occurrences, preserving repeated input.
func (e *Editor) RestoreHistory(source string, draft Draft) {
	for i := range e.InputHistory {
		entry := &e.InputHistory[i]
		if entry.Source == source && entry.Text == draft.Text && !entry.Restored {
			if len(entry.Images)+len(entry.Files)+len(entry.Skills)+len(entry.Selections) == 0 {
				entry.Draft = draft.Clone()
			}
			entry.Restored = true
			return
		}
	}
	e.Remember(draft)
	if draft.Text != "" {
		entry := &e.InputHistory[len(e.InputHistory)-1]
		entry.Source, entry.Restored = source, true
	}
}

// PrependHistory reconciles restored occurrences in shared chronological order.
// Live acknowledgements remain newer; parked and undo navigation follows the
// same entries when enrichment moves a restored occurrence.
func (e *Editor) PrependHistory(entries []HistoryEntry, parked ...*State) {
	positions := make([]int, len(e.InputHistory))
	for i := range positions {
		positions[i] = -1
	}
	var shared []HistoryEntry
	for _, entry := range entries {
		for i, local := range e.InputHistory {
			if positions[i] < 0 && local.Restored && local.Source == entry.Source && local.Text == entry.Text {
				positions[i] = len(shared)
				entry = local
				break
			}
		}
		shared = append(shared, entry)
	}
	var history []HistoryEntry
	// Restored records absent from the bounded shared tail precede that tail.
	for i, entry := range e.InputHistory {
		if positions[i] < 0 && entry.Restored {
			positions[i] = len(history) + len(shared)
			history = append(history, entry)
		}
	}
	prefix := len(history)
	for i, position := range positions {
		if position >= len(shared) {
			positions[i] -= len(shared)
		} else if position >= 0 {
			positions[i] += prefix
		}
	}
	history = append(history, shared...)
	for i, entry := range e.InputHistory {
		if positions[i] < 0 {
			positions[i] = len(history)
			history = append(history, entry)
		}
	}
	rebase := func(back *int) {
		if *back > 0 && *back <= len(positions) {
			*back = len(history) - positions[len(positions)-*back]
			if *back > 100 {
				*back = 0
			}
		}
	}
	rebase(&e.HistoryBack)
	for _, stack := range [][]Undo{e.UndoDrafts, e.RedoDrafts} {
		for i := range stack {
			rebase(&stack[i].HistoryBack)
		}
	}
	for _, state := range parked {
		rebase(&state.Snapshot.HistoryBack)
		for _, stack := range [][]Undo{state.UndoDrafts, state.RedoDrafts} {
			for i := range stack {
				rebase(&stack[i].HistoryBack)
			}
		}
	}
	e.InputHistory = slices.Clone(history[max(0, len(history)-100):])
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
		snapshot = e.InputHistory[len(e.InputHistory)-next].Draft
	}
	e.Replace(snapshot)
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
	e.Replace(snapshot.Draft)
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
	e.Replace(Draft{})
	e.HistoryBack, e.HistoryDraft = 0, Draft{}
	e.UndoDrafts, e.RedoDrafts = nil, nil
	e.Run = NoRun
	return draft
}
func (e *Editor) Restore(parts ...Draft) {
	e.UndoDrafts, e.RedoDrafts = nil, nil
	e.Run = NoRun
	e.Replace(Join(append(slices.Clone(parts), e.Draft.Editable())...))
}
func (e *Editor) Park() State { return State{e.Snapshot(), e.UndoDrafts, e.RedoDrafts, e.Run} }
func (e *Editor) Resume(s State) {
	e.Replace(s.Snapshot.Draft)
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
