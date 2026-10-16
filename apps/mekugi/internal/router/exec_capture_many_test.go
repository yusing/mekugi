package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

// A file-count limit must not make a small, enumerable edit batch incomplete.

func TestPythonGlobManyCandidatesOnlyReportsWrites(t *testing.T) {
	workspace := t.TempDir()
	for i := range 512 {
		writeTestFile(t, filepath.Join(workspace, "src", fmt.Sprintf("file-%03d.py", i)), "old\n")
	}
	script := `from pathlib import Path
for source in Path("src").glob("*.py"):
    if source.name.endswith("0.py"):
        source.write_text(source.read_text().replace("old", "new"))
`
	writeTestFile(t, filepath.Join(workspace, "edit.py"), script)
	pythonName, pythonBinary := interpreterForTest("python3", "python")
	observation := captureInterpreterTestObservation(t, workspace, pythonName+" edit.py")
	if len(observation.Omitted) != 0 || len(observation.Files) < 512 {
		t.Fatalf("Python candidate baselines=%d omitted=%+v", len(observation.Files), observation.Omitted)
	}
	runOrSimulateInterpreter(t, pythonBinary, workspace, "edit.py", func() {
		for i := range 512 {
			if i%10 == 0 {
				writeTestFile(t, filepath.Join(workspace, "src", fmt.Sprintf("file-%03d.py", i)), "new\n")
			}
		}
	})
	result := changecapture.Reconcile(t.Context(), *observation, changecapture.Comparison{})
	reviews, complete, coverage := result.Files, result.Complete, result.Coverage
	if !complete || coverage != changecapture.CoverageExact || len(reviews) != 52 {
		t.Fatalf("Python writes=%d complete=%v coverage=%s", len(reviews), complete, coverage)
	}
	for _, review := range reviews {
		if !strings.Contains(review.Diff, "+new") || !strings.Contains(review.Diff, "-old") {
			t.Fatalf("unexpected candidate review: %+v", review)
		}
	}
}

func TestExecCaptureManyNewDestinations(t *testing.T) {
	workspace := t.TempDir()
	out := filepath.Join(workspace, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 512 {
		writeTestFile(t, filepath.Join(workspace, "src", fmt.Sprintf("file-%03d.txt", i)), "content\n")
	}
	observation := observeTestCommand(t, workspace, "cp src/*.txt out/")
	if len(observation.Omitted) != 0 {
		t.Fatalf("destination capture omitted: %+v", observation.Omitted)
	}
	for i := range 512 {
		writeTestFile(t, filepath.Join(out, fmt.Sprintf("file-%03d.txt", i)), "content\n")
	}
	result := changecapture.Reconcile(t.Context(), *observation, changecapture.Comparison{})
	reviews, complete, coverage := result.Files, result.Complete, result.Coverage
	if !complete || coverage != changecapture.CoverageExact || len(reviews) != 512 {
		t.Fatalf("destination reviews=%d complete=%v coverage=%s", len(reviews), complete, coverage)
	}
}
