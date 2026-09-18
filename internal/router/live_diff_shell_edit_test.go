package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func waitLiveDiffWorkerPreview(t *testing.T, broker *liveDiffBroker, sub *liveDiffSubscriber, match func(liveDiffPreview) bool) liveDiffPreview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-sub.previewReady:
			for _, event := range broker.takePreviews(sub) {
				if event.Preview != nil && match(*event.Preview) {
					return *event.Preview
				}
			}
		case <-timer.C:
			t.Fatal("missing live diff worker preview")
		}
	}
}

func newLiveDiffWorkerTest(t *testing.T, workspace string) (*liveDiffBroker, *liveDiffSubscriber, *liveDiffPreviewWorker) {
	t.Helper()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)
	return broker, sub, worker
}

func TestLiveDiffShellEditLiteralInputs(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"\n# edit\nhpatch<<'EDIT' # literal input\nnew file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hello\"\nED", "new file.txt\ntype \"hello\"\n"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hello\"\nEDIT\n", "new file.txt\ntype \"hello\"\n"},
		{"hpatch <<-'EDIT'\n\tnew file.txt\n\ttype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch 'new file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{`hpatch $'new file.txt\ntype "hello"'`, "new file.txt\ntype \"hello\""},
		{"hpatch \"new file.txt\ntype \\\"hel", "new file.txt\ntype \"hel"},
		{"#!sh\nhpatch <<'EDIT'\nnew file.txt\ntype \"$literal", "new file.txt\ntype \"$literal"},
		{"hpatch <<EDIT\nnew file.txt\ntype \"hello\"\n", "new file.txt\ntype \"hello\"\n"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			directory := t.TempDir()
			got, base, ok := liveDiffShellEdit(tc.input, directory)
			if !ok || got != tc.want || base != directory {
				t.Fatalf("decode = %q, %q, %t; want %q", got, base, ok, tc.want)
			}
			files, err := mekugi.PreviewForHostAt(t.Context(), base, got)
			if err != nil || len(files) != 1 || !strings.Contains(files[0].Diff, "+") {
				t.Fatalf("preview = %+v, %v", files, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preview modified workspace: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerKeepsLastValidHpatchDiff(t *testing.T) {
	for _, tc := range []struct{ name, header string }{
		{"spaced heredoc", "hpatch <<'EDIT'\n"},
		{"no-space heredoc", "hpatch<<'EDIT'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
			worker.appendDelta(tc.header + "in file.txt\ntype \"old\" \"new\"")
			good := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
				return preview.Status == "STREAMING PREVIEW"
			})
			if len(good.Files) != 1 || good.Input != "" || len(good.Syntax) != 0 || !strings.Contains(good.Files[0].Diff, "+new") {
				t.Fatalf("valid hpatch preview = %+v", good)
			}

			worker.appendDelta("\nin file.txt\ntype \"target that does not exist\" \"rejected\"\nEDIT\n")
			invalid := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
				return strings.Contains(preview.Status, "last valid diff")
			})
			if invalid.Status != "STREAMING PREVIEW: last valid diff; current edit unavailable" ||
				invalid.Input != "" || len(invalid.Syntax) != 0 || len(invalid.Files) != 1 ||
				!strings.Contains(invalid.Files[0].Diff, "+new") {
				t.Fatalf("invalid hpatch preview did not retain last diff = %+v", invalid)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerReportsUnavailableInvalidHpatch(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch<<'EDIT'\nin file.txt\ntype \"target that does not exist\" \"rejected\"")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "PREVIEW UNAVAILABLE: edit cannot be projected"
	})
	if preview.Input != "" || len(preview.Syntax) != 0 || len(preview.Files) != 0 {
		t.Fatalf("unprojectable first hpatch leaked source or files = %+v", preview)
	}
}

func TestLiveDiffPreviewWorkerClearsDiffForCompoundShell(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch<<'EDIT'\nin file.txt\ntype \"old\" \"new\"\nEDIT\n")
	good := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW"
	})
	if len(good.Files) != 1 || !strings.Contains(good.Files[0].Diff, "+new") {
		t.Fatalf("valid hpatch preview = %+v", good)
	}

	worker.appendDelta("; echo after")
	shell := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING SCRIPT"
	})
	if len(shell.Files) != 0 || shell.Input == "" || !strings.Contains(shell.Input, "; echo after") || len(shell.Syntax) == 0 {
		t.Fatalf("compound shell retained hpatch diff or lost source = %+v", shell)
	}
}

func TestLiveDiffPreviewBrokerRetainsDisplayedDiffAfterOversizedProjection(t *testing.T) {
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch<<'EDIT'\nnew small.txt\ntype \"small\"\n")
	small := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW"
	})
	if len(small.Files) != 1 || !strings.Contains(small.Files[0].Diff, "+small") {
		t.Fatalf("small hpatch preview = %+v", small)
	}

	large := strings.Repeat("x", 60<<10)
	worker.appendDelta("new large.txt\ntype <<PATCH\n" + large + "\nPATCH\n")
	oversized := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Status, "last valid diff")
	})
	if oversized.Status != "STREAMING PREVIEW: last valid diff; current edit unavailable" ||
		len(oversized.Files) != 1 || !strings.Contains(oversized.Files[0].Diff, "+small") ||
		strings.Contains(oversized.Files[0].Diff, "+x") {
		t.Fatalf("oversized projection displaced the displayed diff = %+v", oversized)
	}

	worker.appendDelta("\nin small.txt\ntype \"target that does not exist\" \"rejected\"")
	invalid := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Status, "last valid diff")
	})
	if invalid.Status != "STREAMING PREVIEW: last valid diff; current edit unavailable" ||
		len(invalid.Files) != 1 || !strings.Contains(invalid.Files[0].Diff, "+small") ||
		strings.Contains(invalid.Files[0].Diff, "+x") {
		t.Fatalf("invalid target displaced the broker-retained diff = %+v", invalid)
	}
}

func TestLiveDiffPreviewWorkerInterruptedStepKeepsDiffMode(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "handler.go"), []byte(liveDiffSimulationHandler), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch<<'EDIT'\nin handler.go\nadd EOF <<PATCH\n\nfunc InterruptedPreview() string {\n")
	first := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW"
	})
	if len(first.Files) == 0 || first.Input != "" || !strings.Contains(first.Files[0].Diff, "InterruptedPreview") {
		t.Fatalf("step 11 initial fragment did not project as diff = %+v", first)
	}
	worker.appendDelta("\treturn \"INTERRUPTED_TIP")
	second := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW" || strings.Contains(preview.Status, "last valid diff")
	})
	if second.Input != "" || len(second.Syntax) != 0 || len(second.Files) == 0 ||
		!strings.Contains(second.Files[0].Diff, "INTERRUPTED_TIP") {
		t.Fatalf("step 11 interrupted fragment fell back to source = %+v", second)
	}
}

func TestLiveDiffShellEditNoDynamicExecution(t *testing.T) {
	for _, input := range []string{
		"hpatch --recover amber 'maple target \"new\"'",
		"hpatch \"$(touch forbidden)\"",
		"hpatch <<EDIT\n$(touch forbidden)\n",
		"hpatch <<EDIT\n$PAYLOAD\n",
		"hpatch < source.patch",
		"hpatch 'new file' > output",
		"echo prefix; hpatch 'new file'",
		"hpatch 'new file'; echo suffix",
		"hpatch 'new file' &",
		"! hpatch 'new file'",
		"PAYLOAD=x hpatch 'new file'",
		"#!python3\nhpatch 'new file'",
		"#!cmd=cat input | {.}\nhpatch 'new file'",
		"hpatch 'new file'\n#!python3\nprint('suffix')",
	} {
		if source, _, ok := liveDiffShellEdit(input, t.TempDir()); ok {
			t.Errorf("projected execution-dependent input %q as %q", input, source)
		}
	}
}

func TestLiveDiffShellEditWorkdir(t *testing.T) {
	directory := t.TempDir()
	source, base, ok := liveDiffShellEdit("#!params="+string(mustMarshalJSON(map[string]any{"workdir": directory}))+"\nhpatch 'new file.txt\ntype \"hello\"'", "")
	if !ok || base != directory {
		t.Fatalf("workdir = %q, %t", base, ok)
	}
	files, err := mekugi.PreviewForHostAt(t.Context(), base, source)
	if err != nil || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "file.txt") {
		t.Fatalf("files = %+v, %v", files, err)
	}
}
