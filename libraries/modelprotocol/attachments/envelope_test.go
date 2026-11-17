package attachments

import (
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotEnvelopeIdentity(t *testing.T) {
	frames := []string{"Attached file \"/work/file\" (UTF-8 bytes 0:4 of 4; " + ReadGuidance + "):\nbody"}
	for _, id := range []string{"", "submission/0"} {
		original := Snapshot{ID: id, Frames: frames}
		text := original.Encode()
		decoded, ok := DecodeSnapshot(text)
		if !ok || !reflect.DeepEqual(decoded, original) {
			t.Fatalf("snapshot roundtrip: %+v %v", decoded, ok)
		}
		if got, ok := Decode(text); !ok || !reflect.DeepEqual(got, frames) {
			t.Fatal("frame-only consumer lost backwards compatibility")
		}
		receipt := decoded.ReceiptID(frames[0])
		if (receipt == "") != (id == "") {
			t.Fatal("legacy snapshot acquired guessed identity")
		}
		decoded.Frames[0] += "changed projection"
		if decoded.ReceiptID(decoded.Frames[0]) != receipt {
			t.Fatal("content transformation changed snapshot identity")
		}
	}
	for _, raw := range []string{
		snapshotPrefix + `{"id":"","frames":["Attached file x"]}` + snapshotSuffix,
		snapshotPrefix + `{"id":"x","frames":[]}` + snapshotSuffix,
		snapshotPrefix + `{"id":"x","frames":["ordinary text"]}` + snapshotSuffix,
		snapshotPrefix + `{"id":"x\u0000y","frames":["Attached file x"]}` + snapshotSuffix,
		strings.TrimSuffix((Snapshot{ID: "x", Frames: frames}).Encode(), snapshotSuffix),
	} {
		if _, ok := DecodeSnapshot(raw); ok {
			t.Fatalf("malformed snapshot accepted: %q", raw)
		}
	}
}

func TestParseRetainedSelectionMarkup(t *testing.T) {
	for _, fixture := range []struct{ frame, label, source, body string }{
		{`<selected-text label="[Selected message]" source="file&amp;&lt;name&gt;&#34;.go&#10;&#13;&#9;">` + "\n" + selectionNotice + "+new\r\n-old\n日本語\n</selected-text>", "[Selected message]", "file&<name>\".go\n\r\t", "+new\r\n-old\n日本語"},
		{`<selected-text label="[Selected diff]">` + "\n" + selectionNotice + "````diff\n</selected-text>\n```diff\n+code\n```\n````\n</selected-text>", "[Selected diff]", "", "</selected-text>\n```diff\n+code\n```"},
		{`<selected-text label="[Selected diff hunk 2]">` + "\n" + selectionNotice + "```diff\n\n```\n</selected-text>", "[Selected diff hunk 2]", "", ""},
	} {
		selection, ok := ParseSelection(fixture.frame)
		if !ok || selection.Label != fixture.label || selection.Source != fixture.source || selection.Body != fixture.body || selection.Header+selection.Body+selection.Footer != fixture.frame {
			t.Fatalf("retained selection changed: %+v", selection)
		}
		if _, ok := DecodeSnapshot((Snapshot{ID: "submitted", Frames: []string{fixture.frame}}).Encode()); !ok {
			t.Fatal("retained selection envelope is unreadable")
		}
	}
	for _, frame := range []string{
		"<selected-text label=\"[Selected message]\">\nbody\n</selected-text>",
		"<selected-text label=\"[Selected message]\">\n" + selectionNotice + "body",
		"<selected-text label=\"[Selected diff]\">\n" + selectionNotice + "```diff\nbody\n</selected-text>",
	} {
		if _, ok := ParseSelection(frame); ok {
			t.Fatalf("malformed selection restored: %q", frame)
		}
	}
}
