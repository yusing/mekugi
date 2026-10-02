package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestLiveDiffEditingToolAttribution(t *testing.T) {
	t.Parallel()
	patch := "*** Begin Patch\n*** Add File: target.txt\n+edited\n*** End Patch\n"
	cat := "cat >target.txt <<'END'\nedited\nEND\n"
	python := "python3 - <<'PY'\nopen('target.txt', 'w').write('edited\\n')\nPY\n"
	for _, tc := range []struct{ name, kind, input, tool string }{
		{"native patch", applyPatchToolName, patch, "apply_patch"},
		{"nested patch", "exec", "text(await tools.apply_patch(" + string(mustMarshalJSON(patch)) + "));", "apply_patch"},
		{"bound nested patch", "exec", "const patch = " + string(mustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));", "apply_patch"},
		{"native cat", nativeExecCommandToolName, string(mustMarshalJSON(map[string]string{"cmd": cat})), "cat"},
		{"nested cat", "exec", "text(await tools.exec_command(" + string(mustMarshalJSON(map[string]string{"cmd": cat})) + "));", "cat"},
		{"native python", nativeExecCommandToolName, string(mustMarshalJSON(map[string]string{"cmd": python})), "python3"},
		{"nested python", "exec", "text(await tools.exec_command(" + string(mustMarshalJSON(map[string]string{"cmd": python})) + "));", "python3"},
		{"unknown nested command", "exec", "text(await tools.exec_command({cmd:computed}));", ""},
		{"read only", nativeExecCommandToolName, `{"cmd":"cat target.txt"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			worker := liveDiffPreviewWorker{ctx: withLiveDiffSources(t.Context()), kind: tc.kind}
			preview, recognized := worker.project(tc.input, workspace, true)
			if preview.Tool != tc.tool || tc.tool != "" && (!recognized || len(preview.Files) != 1) {
				t.Fatalf("attribution = %+v, recognized=%v; want %q", preview, recognized, tc.tool)
			}
			if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
				t.Fatalf("projection executed a tool: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffNestedToolAttributionSurvivesStreamingCompletion(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)
	command := "cat >target.txt <<'END'\nfirst\nsecond\nEND\n"
	input := "text(await tools.exec_command(" + string(mustMarshalJSON(map[string]string{"cmd": command})) + "));"
	point := strings.Index(input, "second")
	worker.appendDelta(input[:point])
	live := waitLiveDiffWorkerPreview(t, broker, sub, func(p diffview.Preview) bool { return len(p.Files) == 1 })
	if live.Tool != "cat" || live.Complete {
		t.Fatalf("stream attributed to executor: %+v", live)
	}
	worker.finish(input)
	final := waitLiveDiffWorkerPreview(t, broker, sub, func(p diffview.Preview) bool { return p.Complete })
	if final.Tool != "cat" || len(final.Files) != 1 || !strings.Contains(final.Files[0].Diff, "+second") {
		t.Fatalf("completed frame lost editor/content: %+v", final)
	}
	<-worker.done
	if _, err := os.Stat(filepath.Join(workspace, "target.txt")); !os.IsNotExist(err) {
		t.Fatalf("preview executed edit: %v", err)
	}
}

func TestLiveDiffRetainedProjectionKeepsEditingTool(t *testing.T) {
	workspace := t.TempDir()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	original := diffview.Preview{ID: "edit", Workspace: workspace, Thread: "thread", Tool: "cat", Status: diffview.PreviewEdit, DiffText: true, Input: "+first\n"}
	broker.publishPreview(original, false)
	broker.takePreviews(sub)
	broker.publishPreview(diffview.Preview{ID: original.ID, Workspace: workspace, Thread: "thread", Tool: "apply_patch", Status: diffview.PreviewEdit, Input: "\n"}, false)
	retained := broker.takePreviews(sub)[0].Preview
	if retained.Tool != "cat" || retained.Input != original.Input {
		t.Fatalf("pending edit relabeled retained diff: %+v", retained)
	}
}

func TestLiveDiffRunningEditingToolAttribution(t *testing.T) {
	for _, codeMode := range []bool{false, true} {
		for _, tc := range []struct{ command, tool string }{
			{"cat >target.txt <<'END'\nedited\nEND\n", "cat"},
			{"cat >target.txt <<'END'\nedited\nEND\n\"$EDITOR\" target.txt", "cat"},
			{"python3 - <<'PY'\nopen('target.txt','w').write('edited\\n')\nPY\n", "python3"},
		} {
			workspace := t.TempDir()
			observation, ok := captureExecObservation([]execCommandInput{{Command: tc.command, Workdir: workspace, Shell: "bash"}}, false, codeMode, execCaptureEnv{directory: workspace})
			if !ok || observation == nil {
				t.Fatal("missing command observation")
			}
			programs := append([]execProgram(nil), observation.Programs...)
			if tool := execPreviewTool(*observation); tool != tc.tool {
				t.Fatalf("Code Mode=%v tool=%q, want %q; observation=%+v", codeMode, tool, tc.tool, observation)
			}
			if !slices.Equal(programs, observation.Programs) {
				t.Fatal("display changed captured program attribution")
			}
		}
	}
	for _, unknown := range []execObservation{{}, {Programs: []execProgram{{Label: "shell", Direct: true}}}} {
		if tool := execPreviewTool(unknown); tool != "" {
			t.Fatalf("unknown command guessed %q", tool)
		}
	}
}
