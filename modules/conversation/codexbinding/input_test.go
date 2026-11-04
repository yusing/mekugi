package codexbinding

import (
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestInputPreservesPartRelativeBindingsAndSnapshot(t *testing.T) {
	var e composer.Editor
	e.InsertImage("image.png")
	e.InsertText(" @code")
	e.Files = []composer.Token{{Start: 10, End: 15, Path: "code"}}
	e.Attachments = []string{"snapshot"}
	data, err := json.Marshal(Input(e.Draft))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"path":"image.png","type":"localImage"},{"text":" @code","text_elements":[{"byteRange":{"start":1,"end":6},"placeholder":"@code"}],"type":"text"},{"text":"snapshot","text_elements":[],"type":"text"}]`
	// Decode both objects so map order is irrelevant while field spelling and null/empty stay exact.
	var gotValue, wantValue any
	if err := json.Unmarshal(data, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("wire: %s", data)
	}
}

func TestCompactedUploadRetainsPlaceholder(t *testing.T) {
	label := "[Uploaded notes.txt]"
	file := composer.UploadAttachment("notes.txt", []byte("uploaded body"))
	file.Start, file.End = 0, len(label)
	draft := composer.Draft{Text: label, Files: []composer.Token{file}}
	draft.SnapshotFiles("")
	if err := draft.IdentifyAttachments("upload"); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(Input(draft))
	if err != nil {
		t.Fatal(err)
	}
	values := CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{
		{ID: "user", Type: "userMessage", Content: content},
		{ID: "boundary", Type: "contextCompaction", Status: "completed"},
	}}})
	if len(values) != 1 || !reflect.DeepEqual(values[0].Placeholders, []string{label}) || len(values[0].Selection.Frames) != 0 {
		t.Fatalf("upload context = %+v", values)
	}
}
