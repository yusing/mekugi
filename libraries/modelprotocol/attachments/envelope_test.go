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

func TestSelectionMarkup(t *testing.T) {
	for _, label := range []string{"[Selected message]", "[Selected diff]", "[Selected diff hunk 2]"} {
		for _, body := range []string{"", "+new\r\n-old\n日本語", "</selected-text>\n```diff\n+code\n```", strings.Repeat("`", Chunk)} {
			source := "file&<name>\".go\n\r\t"
			frame := FormatSelection(label, source, body)
			selection, ok := ParseSelection(frame)
			if !ok || selection.Label != label || selection.Source != source || selection.Body != body || selection.Header+selection.Body+selection.Footer != frame {
				t.Fatalf("selection roundtrip failed for %q, body length %d", label, len(body))
			}
			if !strings.HasPrefix(frame, `<selected-text label="`+label+`" source="file&amp;&lt;name&gt;&#34;.go&#10;&#13;&#9;">`) || !strings.HasSuffix(frame, "\n</selected-text>") {
				t.Fatalf("selection boundaries or attributes changed: %q", frame[:min(200, len(frame))])
			}
			if strings.HasPrefix(label, "[Selected diff") != strings.HasSuffix(selection.Header, "diff\n") {
				t.Fatalf("diff fence classification: %q", selection.Header[:min(200, len(selection.Header))])
			}
			if _, ok := DecodeSnapshot((Snapshot{ID: "submitted", Frames: []string{frame}}).Encode()); !ok {
				t.Fatal("framed selection cannot travel through the snapshot envelope")
			}
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
