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

func TestLiveDiffPreviewWorkerCompoundFileWritesStayFilePreview(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("mkdir -p generated\ncat >first.txt <<'END'\nfirst")
	first := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
		return p.Status == "STREAMING PREVIEW" && len(p.Files) == 1
	})
	if first.Input != "" || len(first.Syntax) != 0 || !strings.Contains(first.Files[0].Diff, "+first") {
		t.Fatalf("first compound write leaked shell source: %+v", first)
	}
	worker.appendDelta("\nEND\ncat >second.txt <<'END'\nsecond")

	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
		return p.Status == "STREAMING PREVIEW" && len(p.Files) == 2
	})
	if preview.Input != "" || len(preview.Syntax) != 0 {
		t.Fatalf("compound writes leaked shell source: %+v", preview)
	}
	got := make(map[string]string)
	for _, file := range preview.Files {
		got[filepath.Base(file.AfterPath)] = file.Diff
	}
	for name, content := range map[string]string{"first.txt": "+first", "second.txt": "+second"} {
		if !strings.Contains(got[name], content) {
			t.Fatalf("%s preview = %q, want %q", name, got[name], content)
		}
	}
	if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
		t.Fatalf("preview caused file effects: %v, %v", entries, err)
	}
}

func TestLiveDiffPreviewWorkerUnsafeCompoundEditsStayUnavailable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input string
		setup       func(*testing.T, string)
	}{
		{
			name:  "changed directory",
			input: "cd sub\ncat >result.txt <<'END'\ncontent\nEND\n",
			setup: func(t *testing.T, workspace string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "repeated path",
			input: "cat >>result.txt <<'END'\none\nEND\ncat >>result.txt <<'END'\ntwo\nEND\n",
			setup: func(t *testing.T, workspace string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("base\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "redirected neutral prefix",
			input: "mkdir -p generated >result.txt\ncat >>result.txt <<'END'\nnew\nEND\n",
			setup: func(t *testing.T, workspace string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "expanded neutral argument",
			input: "mkdir -p \"$(printf generated)\"\ncat >result.txt <<'END'\nnew\nEND\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if test.setup != nil {
				test.setup(t, workspace)
			}
			broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
			worker.appendDelta(test.input)
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
				return strings.HasPrefix(p.Status, "PREVIEW UNAVAILABLE:")
			})
			if preview.Input != "" || len(preview.Syntax) != 0 || len(preview.Files) != 0 {
				t.Fatalf("unsafe compound edit leaked source or projected stale files: %+v", preview)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerCompleteWriteSurvivesIncompleteSuffix(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("cat >result.txt <<'END'\ncontent\nEND\nif true; then\n")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(p liveDiffPreview) bool {
		return p.Status == "STREAMING PREVIEW" && len(p.Files) == 1
	})
	if preview.Input != "" || len(preview.Syntax) != 0 || !strings.Contains(preview.Files[0].Diff, "+content") {
		t.Fatalf("incomplete suffix leaked completed write source: %+v", preview)
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
