package composer

import (
	"crypto/rand"
	"os"
	"path/filepath"
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

func TestSnapshotSourcesUploadAndPaths(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "host.txt")
	if err := os.WriteFile(path, []byte("host snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	data := []byte("uploaded snapshot")
	var draft Draft
	draft.SnapshotSources(workspace, []AttachmentSource{
		{Name: "../../host.txt", Data: data},
		{Path: "host.txt"},
		{Path: path}, // Native path deduplication is preserved.
		{Name: "../../host.txt", Data: []byte("second upload")},
	})
	data[0] = 'X'
	if err := os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	frames, ok := attachments.Decode(draft.Attachments[0])
	if !ok || len(frames) != 3 || !strings.Contains(frames[0], "uploaded snapshot") || !strings.Contains(frames[1], "host snapshot") || !strings.Contains(frames[2], "second upload") {
		t.Fatalf("ordered immutable snapshots: %q", frames)
	}
	if !strings.Contains(frames[0], `"upload 1: ../../host.txt" (UTF-8 bytes`) || !strings.Contains(frames[0], "uploaded file content, not a host path") || strings.Contains(frames[0], workspace) {
		t.Fatalf("upload name became a host path: %q", frames[0])
	}
	if err := draft.IdentifyAttachments("submission"); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := attachments.DecodeSnapshot(draft.Attachments[0])
	if !ok || snapshot.ReceiptID(snapshot.Frames[0]) == snapshot.ReceiptID(snapshot.Frames[2]) {
		t.Fatal("uploads sharing a display name shared a content receipt")
	}
}

func TestSnapshotUploadOmissions(t *testing.T) {
	for _, data := range [][]byte{{0}, {0xff}, []byte(strings.Repeat("x", attachments.Budget/2+1))} {
		var draft Draft
		draft.SnapshotSources("", []AttachmentSource{{Name: "upload", Data: data}})
		frames, ok := attachments.Decode(draft.Attachments[0])
		if !ok || draft.AttachmentNotice == "" || !strings.Contains(frames[0], "CONTENT NOT ATTACHED") {
			t.Fatalf("missing omission: %+v", draft)
		}
	}
	var draft Draft
	draft.SnapshotSources("", []AttachmentSource{{Name: "first", Data: []byte(strings.Repeat("x", attachments.Budget/3))}, {Name: "second", Data: []byte(strings.Repeat("y", attachments.Budget/3))}})
	frames, ok := attachments.Decode(draft.Attachments[0])
	if !ok || len(draft.Attachments[0]) > attachments.Budget || draft.AttachmentNotice == "" || !strings.Contains(frames[len(frames)-1], "CONTENT NOT ATTACHED") {
		t.Fatalf("uploads did not share budget: %+v", draft)
	}
}

func TestUploadedFileRestoresThroughComposer(t *testing.T) {
	const label = "[Uploaded notes.txt]"
	file := UploadAttachment("notes.txt", []byte("retained upload"))
	file.Start, file.End = 0, len(label)
	draft := Draft{Text: label, Files: []Token{file}}
	draft.SnapshotFiles("")
	var editor Editor
	editor.Restore(draft)
	retry := editor.Take()
	retry.SnapshotFiles("")
	if len(retry.Files) != 1 || retry.Files[0].Path != "" || len(retry.Attachments) != 1 || retry.Attachments[0] != draft.Attachments[0] {
		t.Fatalf("restored upload was lost or became a path: %+v", retry)
	}
	if err := retry.IdentifyAttachments("submission"); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := attachments.DecodeSnapshot(retry.Attachments[0])
	if !ok || snapshot.ReceiptID(snapshot.Frames[0]) == "" {
		t.Fatal("uploaded file lost the existing content receipt")
	}
}
