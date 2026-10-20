package composer

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func TestIdentifyAttachmentBudget(t *testing.T) {
	frames := FileFrames("/workspace/file", strings.Repeat("x", attachments.Chunk))
	for len(attachments.Encode(frames))+len(frames[0])+4 < attachments.Budget-512 {
		frames = append(frames, frames[0])
	}
	gap := attachments.Budget - 512 - len(attachments.Encode(frames))
	frames = append(frames, FileFrames("/workspace/tail", strings.Repeat("y", max(0, gap-256)))...)
	original := attachments.Encode(frames)
	if len(original) < attachments.Budget-1024 || len(original) > attachments.Budget-512 {
		t.Fatalf("fixture not near reserved limit: %d", len(original))
	}
	d := Draft{Attachments: []string{original}}
	if err := d.IdentifyAttachments(rand.Text()); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := attachments.DecodeSnapshot(d.Attachments[0])
	if !ok || snapshot.ID == "" || attachments.Encode(snapshot.Frames) != original {
		t.Fatal("near-budget snapshot became ordinary input or changed content")
	}
	// An older or supplied envelope can fill the entire old budget. Reject
	// stamping explicitly and leave its original snapshot available for retry.
	extra := attachments.Budget - len(original)
	frames[len(frames)-1] += strings.Repeat("z", extra)
	full := attachments.Encode(frames)
	if _, ok := attachments.Decode(full); !ok {
		t.Fatal("full legacy fixture is invalid")
	}
	d.Attachments = []string{full}
	if err := d.IdentifyAttachments(rand.Text()); err == nil || d.Attachments[0] != full {
		t.Fatal("over-budget stamping silently corrupted or consumed the legacy envelope")
	}
}
