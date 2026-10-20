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
