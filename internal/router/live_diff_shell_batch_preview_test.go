package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveDiffPreviewWorkerBatchedFileEdits(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{
		"hpatch second.txt <<'EDIT'\ntype \"old\" \"new",
		"cat >second.txt <<'END'\nnew",
	} {
		t.Run(suffix, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "second.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
			worker.appendDelta("cat >first.txt <<'END'\nfirst\nEND\n#!bash\n" + suffix)
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
				return p.Status == "STREAMING PREVIEW"
			})
			if preview.Input != "" || len(preview.Files) != 1 || preview.Files[0].AfterPath != path ||
				!strings.Contains(preview.Files[0].Diff, "+new") {
				t.Fatalf("batched preview = %+v", preview)
			}
			if _, err := os.Stat(filepath.Join(workspace, "first.txt")); !os.IsNotExist(err) {
				t.Fatalf("preview created first file: %v", err)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerComposedHpatchDoesNotLeakScript(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"cat >first.txt <<'END'\nfirst\nEND\nhpatch second.txt <<'EDIT'\ntype \"old\" \"new\"\nEDIT\n",
		`echo before && hpatch second.txt 'type "old" "new"'`,
		`hpatch second.txt 'type "old" "new"' | cat`,
		`{ hpatch second.txt 'type "old" "new"'; }`,
		`(hpatch second.txt 'type "old" "new"')`,
		`hpatch second.txt 'type "old" "new"' &`,
	} {
		t.Run(input, func(t *testing.T) {
			workspace := t.TempDir()
			broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
			worker.appendDelta(input)
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
				return strings.HasPrefix(p.Status, "PREVIEW UNAVAILABLE:")
			})
			if preview.Input != "" || len(preview.Files) != 0 {
				t.Fatalf("invalid composed edit leaked source or speculative files: %+v", preview)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preview caused file effects: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerBatchScriptOmitsEarlierEdits(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch file.txt 'type \"old\" \"new\"'\n#!bash\necho done\n")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
		return p.Status == "STREAMING SCRIPT"
	})
	if preview.Input != "#!bash\necho done\n" || len(preview.Files) != 0 {
		t.Fatalf("batch script fallback exposed earlier edit payload: %+v", preview)
	}
}
