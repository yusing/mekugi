package composer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMixedTokenEditsAndJoin(t *testing.T) {
	var e Editor
	e.InsertImage("first.png")
	e.InsertText(" @code $review ")
	e.Files = []Token{{Start: 10, End: 15, Path: "code"}}
	e.Skills = []Token{{Start: 16, End: 23, Name: "review", Path: "review/SKILL.md"}}
	if err := e.InsertSelection("message", "quoted", "", nil); err != nil {
		t.Fatal(err)
	}
	original := e.Draft.Clone()
	e.CursorBack = len(e.Text)
	e.InsertImage("before.png")
	if e.Text != "[Image 1][Image 2] @code $review [Selected message] " {
		t.Fatalf("insert: %q", e.Text)
	}
	for _, span := range e.Spans() {
		if e.Boundary(span.Start, 1) != span.End || e.Boundary(span.End, -1) != span.Start {
			t.Fatalf("split token: %+v", span)
		}
	}
	e.Delete(0, len("[Image 1]"), NoRun)
	if !reflect.DeepEqual(e.Draft.Editable(), Draft{Text: original.Text, CursorBack: len(original.Text), Images: original.Images, Files: original.Files, Skills: original.Skills, Selections: original.Selections}) {
		t.Fatalf("delete: %+v", e.Draft)
	}
	joined := Join(original, original)
	if len(joined.Images) != 2 || joined.Images[1].Path != "first.png" || joined.Text[joined.Files[1].Start:joined.Files[1].End] != "@code" {
		t.Fatalf("join: %+v", joined)
	}
	joined.Images[0].Path = "mutated"
	if original.Images[0].Path != "first.png" {
		t.Fatal("join mutated retained input")
	}
}

func TestImageRenumberCrossesDigitBoundary(t *testing.T) {
	var e Editor
	for i := range 10 {
		e.InsertImage(fmt.Sprintf("%d.png", i))
	}
	e.InsertText(" @tail")
	e.Files = []Token{{Start: len(e.Text) - 5, End: len(e.Text), Path: "tail"}}
	e.Delete(0, len("[Image 1]"), NoRun)
	if strings.Contains(e.Text, "[Image 10]") || e.Text[e.Files[0].Start:e.Files[0].End] != "@tail" || e.Images[8].Path != "9.png" {
		t.Fatalf("renumber: %+v", e.Draft)
	}
	e.Undo(false)
	if !strings.Contains(e.Text, "[Image 10]") || len(e.Images) != 10 {
		t.Fatalf("undo: %+v", e.Draft)
	}
}

func TestUnicodeAndAtomicBoundaries(t *testing.T) {
	var e Editor
	e.Insert("a👩‍💻")
	e.InsertImage("image.png")
	e.Insert("\u0301z")
	image := e.Images[0]
	if e.Boundary(image.Start, -1) != 1 || e.Boundary(image.End, 1) != image.End+len("\u0301") {
		t.Fatal("grapheme boundary changed")
	}
	if e.WordBoundary(image.End, true) != image.Start || e.WordBoundary(image.Start, false) != image.End {
		t.Fatal("word split image")
	}
	e.Delete(image.End, e.Boundary(image.End, 1), NoRun)
	if len(e.Images) != 1 || e.Text != "a👩‍💻[Image 1]z" {
		t.Fatalf("combining-mark deletion: %+v", e.Draft)
	}
}

func TestHistoryAndParkedEditors(t *testing.T) {
	var e Editor
	e.Remember(Draft{Text: "submitted"})
	e.Insert("main draft")
	e.CursorBack = 2
	main := e.Park()
	e.Resume(State{})
	e.InsertImage("answer.png")
	answer := e.Park()
	e.Resume(main)
	if e.Text != "main draft" || e.CursorBack != 2 || len(e.InputHistory) != 1 {
		t.Fatalf("parked main: %+v", e)
	}
	if !e.Recall(true) || e.Text != "submitted" {
		t.Fatal("recall failed")
	}
	e.Undo(false)
	if e.Text != "main draft" || e.HistoryBack != 0 || e.CursorBack != 2 {
		t.Fatalf("undo recall: %+v", e)
	}
	e.Resume(answer)
	e.Delete(0, len(e.Text), NoRun)
	e.Undo(false)
	if len(e.Images) != 1 || e.Images[0].Path != "answer.png" {
		t.Fatalf("answer undo: %+v", e)
	}
	e.Resume(main)
	if e.Text != "main draft" || len(e.Images) != 0 {
		t.Fatal("answer leaked into main")
	}
}

func TestSnapshotsDetachAndRecallRefreshesAttachments(t *testing.T) {
	var e Editor
	e.InsertImage("source.png")
	e.Attachments = []string{"old snapshot"}
	e.Remember(e.Draft)
	e.Images[0].Path = "changed.png"
	e.Take()
	e.Recall(true)
	if e.Images[0].Path != "source.png" || len(e.Attachments) != 0 {
		t.Fatalf("recall: %+v", e.Draft)
	}
	if e.InputHistory[0].Attachments[0] != "old snapshot" {
		t.Fatal("immutable history lost snapshot")
	}
	e.Restore(Draft{Text: "queued", Attachments: []string{"queued snapshot"}})
	if e.Text != "queued\n[Image 1]" {
		t.Fatalf("restore: %q", e.Text)
	}
}

func TestExternalEditPreservesBindingsAtomically(t *testing.T) {
	var e Editor
	e.InsertImage("image.png")
	if err := e.InsertSelection("message", "quote", "file.go", nil); err != nil {
		t.Fatal(err)
	}
	before := e.Draft.Clone()
	if err := e.ApplyExternal(e.Text + "[Image 1]"); err == nil {
		t.Fatal("accepted duplicate image")
	}
	if !reflect.DeepEqual(before, e.Draft) {
		t.Fatal("rejected edit mutated draft")
	}
	if err := e.ApplyExternal("[Selected message] [Image 1]"); err != nil {
		t.Fatal(err)
	}
	if e.Selections[0].Start != 0 || e.Images[0].Start != len("[Selected message] ") {
		t.Fatalf("moved bindings: %+v", e.Draft)
	}
	e.Undo(false)
	if !reflect.DeepEqual(before, e.Draft) {
		t.Fatal("editor undo did not restore bindings")
	}
}

func TestHistoryRestorationPreservesRepeatedOccurrencesAndSources(t *testing.T) {
	for _, sharedFirst := range []bool{false, true} {
		e := Editor{}
		persisted := []HistoryEntry{
			{Text: "same", Source: "other"},
			{Text: "same", Source: "session"},
			{Text: "same", Source: "session"},
		}
		if sharedFirst {
			e.PrependHistory(persisted)
		}
		e.RestoreHistory("session", Draft{Text: "same", Skills: []Token{{Name: "first"}}})
		e.RestoreHistory("session", Draft{Text: "same", Skills: []Token{{Name: "second"}}})
		if !sharedFirst {
			e.PrependHistory(persisted)
		}
		if len(e.InputHistory) != 3 || e.InputHistory[0].Source != "other" || e.InputHistory[1].Skills[0].Name != "first" || e.InputHistory[2].Skills[0].Name != "second" {
			t.Fatalf("sharedFirst=%v history=%+v", sharedFirst, e.InputHistory)
		}
	}
}

func TestHistoryMergeRebasesParkedAndUndoNavigation(t *testing.T) {
	e := Editor{Text: "draft", CursorBack: 2}
	e.RestoreHistory("main", Draft{Text: "old"})
	e.Recall(true)
	parked := e.Park()
	e.Recall(false)
	e.PrependHistory([]HistoryEntry{{Text: "old", Source: "main"}, {Text: "new", Source: "other"}}, &parked)
	e.Undo(false)
	if e.Text != "old" || e.HistoryBack != 2 {
		t.Fatalf("undo=%+v", e)
	}
	e.Resume(parked)
	e.Recall(false)
	if e.Text != "new" {
		t.Fatalf("parked history skipped newest: %q", e.Text)
	}
	e.Recall(false)
	if e.Text != "draft" || e.CursorBack != 2 {
		t.Fatalf("parked draft=%+v", e.Draft)
	}
}
