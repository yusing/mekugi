package capture

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestBunFormatterOutsideWorkspaceContinuesCapturedChain(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	workdir := filepath.Join(external, "app")
	path := filepath.Join(workdir, "component.ts")
	writeTestFile(t, path, "const value='before';\n")
	var composition diffmodel.ReviewComposition
	for _, step := range []struct{ command, directory, after string }{
		{"sed -i 's/before/after/' component.ts", workdir, "const value='after';\n"},
		{"cd '" + external + "' && grep fmt Makefile | head; cd app && bun fmt component.ts 2>&1 | tail -5", workspace, "const value = \"after\";\n"},
		{"sed -i 's/after/done/' component.ts", workdir, "const value = \"done\";\n"},
	} {
		observation, _ := Capture(t.Context(), []commandanalysis.Command{{Command: step.command, Workdir: step.directory, Shell: "bash"}}, false, false, Options{Directory: workspace})
		if observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != path {
			t.Fatalf("%s lost its named baseline outside the fallback workspace: %+v", step.command, observation)
		}
		writeTestFile(t, path, step.after)
		result := Reconcile(t.Context(), *observation, Comparison{})
		if !result.Complete || len(result.Files) != 1 {
			t.Fatalf("%s capture: %+v", step.command, result)
		}
		if err := composition.ApplyWithHighlight(result.Files[0], false, false); err != nil {
			t.Fatalf("%s broke composition: %v", step.command, err)
		}
	}
	files := composition.Files()
	if len(files) != 1 || !strings.Contains(files[0].Diff, "-const value='before';") || !strings.Contains(files[0].Diff, "+const value = \"done\";") {
		t.Fatalf("composed edits = %+v", files)
	}
}

// Tests and generators have no derived write scope: their effects are window
// evidence from the workspace inventory, never an explicit claim.

func TestFormatterDeferredHintsStayInsideExplicitScope(t *testing.T) {
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	inside := filepath.Join(workspace, "format.go")
	outside := filepath.Join(elsewhere, "format.go")
	writeTestFile(t, inside, "package p\n")
	writeTestFile(t, outside, "package p\n")
	capture := newExecCapture(time.Now().Add(-time.Second))
	capture.scopeRoots = []string{workspace}
	capture.origin = "gofmt"
	capture.add(inside)
	capture.add(outside)
	if len(capture.omitted) != 2 || !capture.omitted[0].Deferred || capture.omitted[1].Deferred {
		t.Fatalf("formatter hints claimed paths outside explicit scope: %+v", capture.omitted)
	}
}

func TestDeferredFormatterHintWithoutClockDoesNotInventDiff(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "package pkg\n")
	observation := Observation{
		Class: execscope.Scoped.String(), Roots: []string{workspace},
		Omitted: []Omission{{Path: path, Origin: "gofmt", Reason: "capture deadline", Deferred: true}},
	}
	result := Reconcile(t.Context(), observation, Comparison{})
	reviews, complete := result.Files, result.Complete
	if complete || len(reviews) != 1 || reviews[0].Incomplete == "" || strings.Contains(reviews[0].Diff, "+package pkg") {
		t.Fatalf("unavailable formatter baseline invented evidence: %+v complete=%v", reviews, complete)
	}
}
