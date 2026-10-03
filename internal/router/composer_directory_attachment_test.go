package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func directoryAttachmentRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Stop inherited ignore discovery at this isolated fixture repository.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func directoryAttachmentWrite(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func directoryAttachmentTree(t *testing.T, root string) string {
	t.Helper()
	tree, err := composerDirectoryTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestDirectoryAttachmentSortedNamesDepthAndMetadata(t *testing.T) {
	root := directoryAttachmentRoot(t)
	for _, name := range []string{"z.txt", "folder/z.txt", "folder/a.txt", "folder/deep/hidden.txt", "a.txt", ".svn/private", ".hg/private", ".bzr/private", "_darcs/private", "CVS/private", "folder/.git/private"} {
		directoryAttachmentWrite(t, root, name, "FILE CONTENT MUST NEVER APPEAR")
	}
	want := "./\n- a.txt\n- folder/\n  - a.txt\n  - deep/\n  - z.txt\n- z.txt\n"
	if got := directoryAttachmentTree(t, root); got != want {
		t.Fatalf("tree = %q, want %q", got, want)
	}
}

func TestDirectoryAttachmentIgnoreScopesAndNegation(t *testing.T) {
	root := directoryAttachmentRoot(t)
	directoryAttachmentWrite(t, root, ".gitignore", "/scope/anchored.txt\n*.tmp\n!keep.tmp\nblocked/\n!blocked/rescue.txt\n")
	directoryAttachmentWrite(t, root, "scope/.gitignore", "/local.txt\n!override.tmp\nnested-only/\n")
	for _, name := range []string{"scope/anchored.txt", "scope/local.txt", "scope/drop.tmp", "scope/keep.tmp", "scope/override.tmp", "scope/blocked/rescue.txt", "scope/nested-only/hidden", "scope/visible.txt", "scope/child/anchored.txt", "scope/child/local.txt", "scope/child/drop.tmp", "scope/child/keep.tmp", "scope/child/nested-only"} {
		directoryAttachmentWrite(t, root, name, "not attached")
	}
	want := "./\n- .gitignore\n- child/\n  - anchored.txt\n  - keep.tmp\n  - local.txt\n  - nested-only\n- keep.tmp\n- override.tmp\n- visible.txt\n"
	if got := directoryAttachmentTree(t, filepath.Join(root, "scope")); got != want {
		t.Fatalf("scoped tree = %q, want %q", got, want)
	}
}

func TestDirectoryAttachmentChildIgnoreDoesNotLeakToSibling(t *testing.T) {
	root := directoryAttachmentRoot(t)
	directoryAttachmentWrite(t, root, "a/.gitignore", "hidden.txt\n")
	for _, name := range []string{"a/hidden.txt", "a/visible.txt", "b/hidden.txt"} {
		directoryAttachmentWrite(t, root, name, "not attached")
	}
	want := "./\n- a/\n  - .gitignore\n  - visible.txt\n- b/\n  - hidden.txt\n"
	if got := directoryAttachmentTree(t, root); got != want {
		t.Fatalf("tree = %q, want %q", got, want)
	}
}

func TestDirectoryAttachmentPOSIXIgnoreMatchesGit(t *testing.T) {
	g := newGitFixture(t)
	directoryAttachmentWrite(t, g.dir, ".gitignore", "secret[[:digit:]].txt\nletter[[:alpha:]].txt\nmixed[[:alpha:]0-9].txt\nnegative[![:digit:]].txt\n")
	ignored := []string{"secret1.txt", "lettera.txt", "mixedz.txt", "mixed5.txt", "negativea.txt"}
	visible := []string{"secreta.txt", "letter1.txt", "mixed-.txt", "negative5.txt"}
	for _, name := range append(ignored, visible...) {
		directoryAttachmentWrite(t, g.dir, name, "not attached")
	}
	oracle := g.run(append([]string{"check-ignore", "--no-index"}, append(slices.Clone(ignored), visible...)...)...)
	if oracle != strings.Join(ignored, "\n")+"\n" {
		t.Fatalf("unexpected Git oracle output: %q", oracle)
	}
	frames, err := frameComposerPath(g.dir)
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(frames[0], ":\n")
	for _, name := range ignored {
		if strings.Contains(body, "- "+name+"\n") {
			t.Fatalf("Git-ignored name included: %q in %q", name, body)
		}
	}
	for _, name := range visible {
		if !strings.Contains(body, "- "+name+"\n") {
			t.Fatalf("unignored name missing: %q in %q", name, body)
		}
	}
}

func TestDirectoryAttachmentRootAliasUsesTargetIgnoreAncestry(t *testing.T) {
	root := directoryAttachmentRoot(t)
	directoryAttachmentWrite(t, root, ".gitignore", "secret.txt\n")
	directoryAttachmentWrite(t, root, "src/secret.txt", "not attached")
	directoryAttachmentWrite(t, root, "src/public.txt", "not attached")
	alias := filepath.Join(t.TempDir(), "selected-alias")
	if err := os.Symlink(filepath.Join(root, "src"), alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "src"), alias} {
		frames, err := frameComposerPath(path)
		if err != nil || len(frames) != 1 || !strings.HasPrefix(frames[0], "Attached file "+strconv.Quote(path)+" (") || !strings.HasSuffix(frames[0], ":\n./\n- public.txt\n") {
			t.Fatalf("selected path/target ignore ancestry lost: frames=%q err=%v", frames, err)
		}
	}
}

func TestDirectoryAttachmentSymlinksAndUnsafeNames(t *testing.T) {
	root := directoryAttachmentRoot(t)
	outside := directoryAttachmentRoot(t)
	directoryAttachmentWrite(t, outside, "outside-secret", "private")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	unsafe := "line\n- forged\t\"\\" + string([]byte{0xff})
	directoryAttachmentWrite(t, root, unsafe, "private")
	want := "./\n- broken (symlink)\n- " + strconv.Quote(unsafe) + "\n- linked (symlink)\n"
	if got := directoryAttachmentTree(t, root); got != want {
		t.Fatalf("tree = %q, want %q", got, want)
	}
	alias := filepath.Join(t.TempDir(), "explicit-root")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	frames, err := frameComposerPath(alias)
	if err != nil || len(frames) != 1 || !strings.HasSuffix(frames[0], ":\n./\n- outside-secret\n") {
		t.Fatalf("explicit root symlink frames = %q, error = %v", frames, err)
	}
}

func TestDirectoryAttachmentBounds(t *testing.T) {
	t.Run("entry limit", func(t *testing.T) {
		root := directoryAttachmentRoot(t)
		for i := range composerTreeEntries + 1 {
			directoryAttachmentWrite(t, root, fmt.Sprintf("file-%04d", i), "")
		}
		tree := directoryAttachmentTree(t, root)
		if strings.Count(tree, "\n- ") != composerTreeEntries || !strings.Contains(tree, "[TRUNCATED:") || strings.Contains(tree, fmt.Sprintf("- file-%04d\n", composerTreeEntries)) {
			t.Fatalf("entry bound lost: %q", tree)
		}
	})
	t.Run("byte limit", func(t *testing.T) {
		root := directoryAttachmentRoot(t)
		for i := range 100 {
			directoryAttachmentWrite(t, root, fmt.Sprintf("%04d-%s", i, strings.Repeat("界", 70)), "")
		}
		tree := directoryAttachmentTree(t, root)
		if len(tree) > composerTreeBytes || !strings.Contains(tree, "[TRUNCATED:") || strings.Count(tree, "\n- ") >= 100 {
			t.Fatalf("byte bound lost: %d bytes", len(tree))
		}
	})
	t.Run("scan includes ignored entries and omits oversized listing", func(t *testing.T) {
		root := directoryAttachmentRoot(t)
		directoryAttachmentWrite(t, root, ".gitignore", "ignored-*\n")
		for i := range composerTreeScan + 1 {
			directoryAttachmentWrite(t, root, fmt.Sprintf("ignored-%04d", i), "")
		}
		directoryAttachmentWrite(t, root, "visible", "")
		tree := directoryAttachmentTree(t, root)
		if !strings.HasPrefix(tree, "./\n[TRUNCATED:") || strings.Contains(tree, "\n- ") || !strings.Contains(tree, "scan limit") {
			t.Fatalf("oversized directory partially listed: %q", tree)
		}
		if again := directoryAttachmentTree(t, root); again != tree {
			t.Fatalf("oversized listing is nondeterministic: %q != %q", tree, again)
		}
	})
	t.Run("aggregate ignore input", func(t *testing.T) {
		root := directoryAttachmentRoot(t)
		padding := "#" + strings.Repeat("x", composerIgnoreBytes/2)
		directoryAttachmentWrite(t, root, ".gitignore", padding)
		directoryAttachmentWrite(t, root, "scope/.gitignore", padding)
		if _, err := composerDirectoryTree(filepath.Join(root, "scope")); err == nil || !strings.Contains(err.Error(), "ignore rules exceed") {
			t.Fatalf("aggregate ignore limit error = %v", err)
		}
	})
}

func TestDirectoryAttachmentSnapshotAndProviderReplay(t *testing.T) {
	root := directoryAttachmentRoot(t)
	directoryAttachmentWrite(t, root, "src/original.go", "FILE CONTENT MUST NEVER APPEAR")
	u, w := newAppServerTestUI()
	u.session.cwd = root
	u.draft = "Review "
	bindComposerFile(u, "@src", "src")
	draft := u.takeDraft()
	if err := os.Rename(filepath.Join(root, "src/original.go"), filepath.Join(root, "src/changed.go")); err != nil {
		t.Fatal(err)
	}
	if err := u.send([]composerDraft{draft}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || len(requests[0].Params.Input) != 2 {
		t.Fatalf("expected prompt and snapshot: %+v", requests)
	}
	input := requests[0].Params.Input
	frames, ok := decodeFileAttachments(input[1].Text)
	if !ok || len(frames) != 1 || !strings.HasPrefix(frames[0], "Attached file ") || !strings.Contains(frames[0], "directory tree, max depth 2") || !strings.HasSuffix(frames[0], ":\n./\n- original.go\n") {
		t.Fatalf("submitted directory snapshot = %q", frames)
	}
	raw := mustMarshalJSON([]any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": input[0].Text},
		map[string]any{"type": "input_text", "text": input[1].Text},
	}}})
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": raw}}
	projectFileAttachments(&request)
	var messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(request.fields["input"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "user" || len(messages[1].Content) != 1 || messages[1].Content[0].Text != frames[0] {
		t.Fatalf("provider directory projection = %+v", messages)
	}
	first := bytes.Clone(request.fields["input"])
	projectFileAttachments(&request)
	if !bytes.Equal(first, request.fields["input"]) {
		t.Fatal("directory projection is not idempotent")
	}
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	replayed := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": raw}}
	projectFileAttachments(&replayed)
	if !sameJSONValue(first, replayed.fields["input"]) {
		t.Fatal("provider replay reopened directory instead of retaining submitted snapshot")
	}
}

func TestDirectoryAttachmentSharedEnvelopeBudget(t *testing.T) {
	root := directoryAttachmentRoot(t)
	draft := composerDraft{}
	for i := range 16 {
		dir := fmt.Sprintf("directory-%02d", i)
		for j := range 80 {
			directoryAttachmentWrite(t, root, filepath.Join(dir, fmt.Sprintf("%04d-%s", j, strings.Repeat("x", 200))), "")
		}
		draft.files = append(draft.files, composerFile{path: dir})
	}
	draft.snapshotFileAttachments(root)
	if len(draft.attachments) != 1 || len(draft.attachments[0]) > fileAttachmentBudget || draft.attachmentNotice == "" {
		t.Fatal("directory attachments bypassed shared envelope budget/notice")
	}
	frames, ok := decodeFileAttachments(draft.attachments[0])
	if !ok || !strings.Contains(strings.Join(frames, "\n"), "CONTENT NOT ATTACHED") || !strings.Contains(frames[0], "directory tree, max depth 2") {
		t.Fatalf("directory snapshot or omission notice missing: %q", frames)
	}
}
