package codexbinding

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"strings"
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

func TestCompactedImagesFollowAdmittedHistoryPrefix(t *testing.T) {
	image := func(id, source string) binding.ThreadItem {
		return binding.ThreadItem{ID: id, Type: "userMessage", Content: []byte(`[{"type":"image","url":` + strconv.Quote(source) + `}]`)}
	}
	before := image("before", "https://example.test/submitted.png")
	embedded := image("embedded", "data:image/png,PRIVATE_PIXELS")
	boundary := binding.ThreadItem{ID: "boundary", Type: "contextCompaction", Status: "completed"}
	after := image("after", "https://example.test/later.png")
	tool := binding.ThreadItem{ID: "tool", Type: "imageGeneration", Content: []byte(`[{"type":"localImage","path":"/tool/generated.png"}]`)}
	for _, items := range [][]binding.ThreadItem{{before, embedded, tool, boundary, after}, {before, embedded, boundary}} {
		values := CompactedAttachments([]binding.HistoryTurn{{Items: items}})
		if len(values) != 2 || values[0].Images[0].Path != "https://example.test/submitted.png" || strings.Contains(values[1].Images[0].Path, "PRIVATE_PIXELS") || values[1].Images[0].Name != "[Image 1]" {
			t.Fatalf("native image prefix lost or pixel/tool evidence retained: %+v", values)
		}
	}
	if got := CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{before, {Type: "contextCompaction", Status: "failed"}}}}); len(got) != 0 {
		t.Fatalf("failed boundary admitted recall: %+v", got)
	}
	if got := CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{boundary, after}}}); len(got) != 0 {
		t.Fatalf("rewound prefix admitted later image: %+v", got)
	}
}
