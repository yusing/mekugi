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
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
	}{
		{"spaced heredoc", "hpatch file.txt <<'EDIT'\ntype \"old\" \"hello\"", "type \"old\" \"hello\""},
		{"no-space heredoc", "hpatch file.txt<<'EDIT'\ntype \"old\" \"hello\"", "type \"old\" \"hello\""},
		{"closed heredoc", "hpatch file.txt <<'EDIT'\ntype \"old\" \"hello\"\nEDIT\n", "type \"old\" \"hello\"\n"},
		{"dash heredoc", "hpatch file.txt <<-'EDIT'\n\ttype \"old\" \"hello\"", "type \"old\" \"hello\""},
		{"single quoted argument", `hpatch file.txt 'type "old" "hello"'`, `type "old" "hello"`},
		{"ansi quoted argument", `hpatch file.txt $'type "old" "hello"'`, `type "old" "hello"`},
		{"double quoted argument", "hpatch file.txt \"type \\\"old\\\" \\\"hello\\\"\"", `type "old" "hello"`},
		{"unclosed script quote", `hpatch file.txt 'type "old" "hello`, `type "old" "hello`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			edits, base, ok := liveDiffShellEdit(tc.input, directory)
			if !ok || base != directory || len(edits) != 1 || edits[0].Path != "file.txt" || edits[0].Script != tc.want {
				t.Fatalf("decode = %+v, %q, %t; want path/script %q", edits, base, ok, tc.want)
			}
			files, err := mekugi.PreviewForHostAt(t.Context(), base, edits)
			if err != nil || len(files) != 1 || !strings.Contains(files[0].Diff, "+") {
				t.Fatalf("preview = %+v, %v", files, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
				t.Fatalf("preview modified workspace: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerKeepsLastValidHpatchDiff(t *testing.T) {
	t.Parallel()
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
			worker.appendDelta(strings.Replace(tc.header, "hpatch", "hpatch file.txt", 1) + "type \"old\" \"new\"")
			good := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
				return preview.Status == "STREAMING PREVIEW"
			})
			if len(good.Files) != 1 || good.Input != "" || len(good.Syntax) != 0 || !strings.Contains(good.Files[0].Diff, "+new") {
				t.Fatalf("valid hpatch preview = %+v", good)
			}

			worker.appendDelta("\ntype \"target that does not exist\" \"rejected\"\nEDIT\n")
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
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch file.txt<<'EDIT'\ntype \"target that does not exist\" \"rejected\"")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "PREVIEW UNAVAILABLE: edit cannot be projected"
	})
	if preview.Input != "" || len(preview.Syntax) != 0 || len(preview.Files) != 0 {
		t.Fatalf("unprojectable first hpatch leaked source or files = %+v", preview)
	}
}

func TestLiveDiffPreviewWorkerRetainsLastValidDiffForCompoundShell(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch file.txt<<'EDIT'\ntype \"old\" \"new\"\nEDIT\n")
	good := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW"
	})
	if len(good.Files) != 1 || !strings.Contains(good.Files[0].Diff, "+new") {
		t.Fatalf("valid hpatch preview = %+v", good)
	}

	worker.appendDelta("; echo after")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool { return true })
	if preview.Status != "STREAMING PREVIEW: last valid diff; current edit unavailable" ||
		len(preview.Files) != 1 || preview.Files[0].Diff != good.Files[0].Diff || preview.Input != "" || len(preview.Syntax) != 0 {
		t.Fatalf("compound shell did not retain explicitly unavailable last diff = %+v", preview)
	}
}

func TestLiveDiffPreviewBrokerRetainsDisplayedDiffAfterOversizedProjection(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch file.txt<<'EDIT'\ntype \"old\" \"small\"\n")
	small := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW"
	})
	if len(small.Files) != 1 || !strings.Contains(small.Files[0].Diff, "+small") {
		t.Fatalf("small hpatch preview = %+v", small)
	}

	large := strings.Repeat("x", 60<<10)
	worker.appendDelta("append <<PATCH\n" + large + "\nPATCH\n")
	oversized := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Status, "last valid diff")
	})
	if oversized.Status != "STREAMING PREVIEW: last valid diff; current edit unavailable" ||
		len(oversized.Files) != 1 || !strings.Contains(oversized.Files[0].Diff, "+small") ||
		strings.Contains(oversized.Files[0].Diff, "+x") {
		t.Fatalf("oversized projection displaced the displayed diff = %+v", oversized)
	}

	worker.appendDelta("\ntype \"target that does not exist\" \"rejected\"")
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
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "handler.go"), []byte(liveDiffSimulationHandler), 0600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	worker.appendDelta("hpatch handler.go<<'EDIT'\nappend <<PATCH\n\nfunc InterruptedPreview() string {\n")
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
	t.Parallel()
	for _, input := range []string{
		"hpatch --recover amber 'maple target \"new\"'",
		"hpatch forbidden.txt \"$(touch forbidden)\"",
		"hpatch forbidden.txt <<EDIT\n$(touch forbidden)\n",
		"hpatch forbidden.txt <<EDIT\n$PAYLOAD\n",
		"hpatch forbidden.txt < source.patch",
		"hpatch forbidden.txt 'type \"old\" \"new\"' > output",
		"echo prefix; hpatch forbidden.txt 'type \"old\" \"new\"'",
		"hpatch forbidden.txt 'type \"old\" \"new\"'; echo suffix",
		"hpatch forbidden.txt 'type \"old\" \"new\"' &",
		"! hpatch forbidden.txt 'type \"old\" \"new\"'",
		"PAYLOAD=x hpatch forbidden.txt 'type \"old\" \"new\"'",
		"#!python3\nhpatch forbidden.txt 'type \"old\" \"new\"'",
		"#!cmd=cat input | {.}\nhpatch forbidden.txt 'type \"old\" \"new\"'",
		"hpatch forbidden.txt 'type \"old\" \"new\"'\n#!python3\nprint('suffix')",
	} {
		if source, _, ok := liveDiffShellEdit(input, t.TempDir()); ok {
			t.Errorf("projected execution-dependent input %q as %q", input, source)
		}
	}
}

func TestLiveDiffShellEditWorkdir(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edits, base, ok := liveDiffShellEdit("#!params="+string(mustMarshalJSON(map[string]any{"workdir": directory}))+"\nhpatch file.txt 'type \"old\" \"hello\"'", "")
	if !ok || base != directory || len(edits) != 1 {
		t.Fatalf("workdir = %+v, %q, %t", edits, base, ok)
	}
	files, err := mekugi.PreviewForHostAt(t.Context(), base, edits)
	if err != nil || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "file.txt") {
		t.Fatalf("files = %+v, %v", files, err)
	}
}

func TestLiveDiffShellEditRejectsUnknownExpansion(t *testing.T) {
	t.Parallel()
	if edits, _, ok := liveDiffShellEdit("hpatch file.txt <<EDIT\ntype \"old\" \"$literal\"", t.TempDir()); ok {
		t.Fatalf("invented expansion: %+v", edits)
	}
}
func TestLiveDiffPreviewWorkerSuccessiveShellFragments(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	input := ""
	var initialDiff string
	for i, fragment := range []string{"hpatch file.txt 'append \"hello\"'", " \\", "\n"} {
		input += fragment
		_, _, projectable := liveDiffShellEdit(input, workspace)
		if projectable != (i != 1) {
			t.Fatalf("fragment %d projectable = %t", i, projectable)
		}
		worker.appendDelta(fragment)
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(liveDiffPreview) bool { return true })
		wantStatus := "STREAMING PREVIEW"
		if i == 1 {
			wantStatus = "STREAMING PREVIEW: last valid diff; current edit unavailable"
		}
		if preview.Status != wantStatus || preview.Input != "" || len(preview.Syntax) != 0 || len(preview.Files) != 1 {
			t.Fatalf("fragment %d flashed raw source or lost file view: %+v", i, preview)
		}
		if i == 0 {
			initialDiff = preview.Files[0].Diff
		} else if preview.Files[0].Diff != initialDiff {
			t.Fatalf("fragment %d changed displayed diff: %+v", i, preview)
		}
	}
	worker.appendDelta(" # pending")
	worker.stop()
	<-worker.done
	worker.appendDelta(" ignored after stop")
	for _, event := range broker.takePreviews(sub) {
		if event.Preview != nil && (event.Preview.Status != "" || event.Preview.Input != "" || len(event.Preview.Files) != 0) {
			t.Fatalf("late preview after stop: %+v", event.Preview)
		}
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if _, exists := broker.previews[worker.preview.ID]; exists {
		t.Fatal("stopped preview retained")
	}
}

func TestLiveDiffPreviewWorkerNeverRecognizedShellStaysScript(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"echo normal",
		"hpatch --recover amber 'maple target \"new\"'",
	} {
		t.Run(input, func(t *testing.T) {
			broker, sub, worker := newLiveDiffWorkerTest(t, t.TempDir())
			worker.appendDelta(input)
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(liveDiffPreview) bool { return true })
			if preview.Status != "STREAMING SCRIPT" || preview.Input != input || len(preview.Syntax) == 0 || len(preview.Files) != 0 {
				t.Fatalf("ordinary shell left script mode: %+v", preview)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerProgressiveRecoveryStaysScript(t *testing.T) {
	t.Parallel()
	broker, sub, worker := newLiveDiffWorkerTest(t, t.TempDir())
	for _, fragment := range []string{"hpatch ", "--re", "cover", " amber", " 'maple target \"new\"'"} {
		worker.appendDelta(fragment)
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(liveDiffPreview) bool { return true })
		if preview.Status != "STREAMING SCRIPT" || preview.Input == "" || len(preview.Syntax) == 0 || len(preview.Files) != 0 {
			t.Fatalf("progressive recovery left script mode after %q: %+v", fragment, preview)
		}
	}
}

func TestLiveDiffPreviewWorkerRecognizesFailedProjection(t *testing.T) {
	t.Parallel()
	broker, sub, worker := newLiveDiffWorkerTest(t, t.TempDir())
	for _, fragment := range []string{"hpatch missing.txt 'type \"old\" \"new\"'", "; echo suffix"} {
		worker.appendDelta(fragment)
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(liveDiffPreview) bool { return true })
		if preview.Status != "PREVIEW UNAVAILABLE: edit cannot be projected" || preview.Input != "" || len(preview.Syntax) != 0 || len(preview.Files) != 0 {
			t.Fatalf("recognized edit returned to script mode: %+v", preview)
		}
	}
}
