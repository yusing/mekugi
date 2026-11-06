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

func TestNestedAttachmentSnapshots(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"root.json": `{"next":"@child.txt"}`, "child.txt": "child body @leaf.txt",
		"leaf.txt": "leaf body @too-deep.txt", "too-deep.txt": "MUST NOT LOAD",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"file", "upload", "selection", "skill"} {
		t.Run(kind, func(t *testing.T) {
			var draft Draft
			switch kind {
			case "file":
				draft.SnapshotSources(dir, []AttachmentSource{{Path: "root.json"}})
			case "upload":
				draft.SnapshotSources(dir, []AttachmentSource{{Name: "../untrusted", Data: []byte(`{"next":"@child.txt"}`)}})
			case "selection":
				draft.Text = "[Selected message]"
				draft.Selections = []Token{{Start: 0, End: len(draft.Text), Text: "@child.txt"}}
				draft.SnapshotSources(dir, nil)
			case "skill":
				draft.Skills = []Token{{Name: "selected"}}
				draft.SnapshotSkillSources(dir, func(_, _ string) ([]byte, bool, error) { return []byte("@child.txt"), true, nil })
			}
			frames, ok := attachments.Decode(draft.Attachments[0])
			body := strings.Join(frames, "\n")
			if !ok || !strings.Contains(body, "child body") || !strings.Contains(body, "leaf body") || strings.Contains(body, "MUST NOT LOAD") || !strings.Contains(body, "depth limit 3 exceeded") || !strings.Contains(draft.AttachmentNotice, "depth limit 3 exceeded") {
				t.Fatalf("nested %s: notice=%q frames=%q", kind, draft.AttachmentNotice, frames)
			}
		})
	}
}

func TestNestedAttachmentFailuresAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("unique child"), 0600); err != nil {
		t.Fatal(err)
	}
	var draft Draft
	draft.SnapshotSources(dir, []AttachmentSource{{Name: "root", Data: []byte("@missing @child @child")}, {Path: "child"}})
	frames, _ := attachments.Decode(draft.Attachments[0])
	body := strings.Join(frames, "\n")
	if !strings.Contains(body, "CONTENT NOT ATTACHED") || strings.Count(body, "unique child") != 1 || draft.AttachmentNotice == "" {
		t.Fatal(body)
	}
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(frames, "\n"), "unique child") {
		t.Fatal("snapshot was not immutable")
	}
}

func TestNestedAttachmentShallowerBranchUsesSnapshot(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"root": "@a @b", "a": "@b", "b": "branch body @c", "c": "reachable leaf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var draft Draft
	draft.SnapshotSources(dir, []AttachmentSource{{Path: "root"}})
	frames, _ := attachments.Decode(draft.Attachments[0])
	body := strings.Join(frames, "\n")
	if !strings.Contains(body, "reachable leaf") || strings.Count(body, "branch body") != 1 {
		t.Fatal(body)
	}
}

func TestNestedManagedSkillsReuseFilesAndWorkspace(t *testing.T) {
	dir := t.TempDir()
	placeholder := filepath.Join(dir, "placeholder")
	if err := os.Mkdir(placeholder, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"directory tree, notes.txt": "@child", "child": "workspace body", "placeholder/child": "WRONG body"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var draft Draft
	draft.SnapshotSources(dir, []AttachmentSource{{Path: "directory tree, notes.txt"}})
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("changed body"), 0600); err != nil {
		t.Fatal(err)
	}
	draft.Skills = []Token{{Name: "chosen", Path: filepath.Join(placeholder, "SKILL.md")}}
	draft.SnapshotSkillSources(dir, func(_, _ string) ([]byte, bool, error) { return []byte("@child"), true, nil })
	frames, _ := attachments.Decode(draft.Attachments[0])
	body := strings.Join(frames, "\n")
	if strings.Count(body, "workspace body") != 1 || strings.Contains(body, "changed body") || strings.Contains(body, "WRONG body") {
		t.Fatal(body)
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

func TestSelectionSnapshotRestoresWithoutCoordinates(t *testing.T) {
	for _, label := range []string{"[Selected message]", "[Selected diff]"} {
		for _, body := range []string{"inspect-session\r\n日本語\n", strings.Repeat("日本語\n", 4000)} {
			draft := Draft{Text: label, Selections: []Token{{Start: 0, End: len(label), Text: body, Source: "line\nbreak.go"}}}
			draft.SnapshotFiles("")
			frames, ok := attachments.Decode(draft.Attachments[0])
			if !ok || len(frames) == 0 {
				t.Fatal("selection snapshot missing")
			}
			var restoredBody strings.Builder
			for _, frame := range frames {
				selection, ok := attachments.ParseSelection(frame)
				if !ok || selection.Label != label || selection.Source != "line\nbreak.go" || strings.Contains(selection.Header, "UTF-8 bytes") || !strings.HasPrefix(selection.Header, "<selected-text ") || !strings.HasSuffix(selection.Footer, "\n</selected-text>") {
					t.Fatalf("selection framing: %+v", selection)
				}
				restoredBody.WriteString(selection.Body)
			}
			if restoredBody.String() != body {
				t.Fatal("selection bytes changed")
			}
			restored := RestoreSelections(label, frames)
			if len(restored) != 1 || restored[0] != draft.Selections[0] {
				t.Fatalf("selection lost on restore: %+v", restored)
			}
		}
	}
	const label = "[Selected message]"
	legacy := "Selected text for \"[Selected message]\" (UTF-8 bytes 0:15 of 15; quoted from the user's screen, not a separate request):\ninspect-session"
	if restored := RestoreSelections(label, []string{legacy}); len(restored) != 1 || restored[0].Text != "inspect-session" {
		t.Fatalf("legacy selection lost on restore: %+v", restored)
	}
}
