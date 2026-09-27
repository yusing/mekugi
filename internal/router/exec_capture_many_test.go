package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A file-count limit must not make a small, enumerable edit batch incomplete.
func TestExecCaptureManyLiteralGlobFiles(t *testing.T) {
	workspace := t.TempDir()
	for i := range 512 {
		writeTestFile(t, filepath.Join(workspace, "many", fmt.Sprintf("file-%03d.txt", i)), "before\n")
	}
	capture := newExecCapture(time.Now().Add(10 * time.Second))
	capture.scopeRoots = []string{workspace}
	capture.entry(execScopeEntry{Kind: execScopeFile, Operands: []execOperand{{Path: filepath.Join(workspace, "many", "*.txt"), Glob: true}}})
	if len(capture.files) != 512 || len(capture.omitted) != 0 {
		t.Fatalf("glob baselines=%d omitted=%+v", len(capture.files), capture.omitted)
	}
	for i := range 12 {
		writeTestFile(t, filepath.Join(workspace, "many", fmt.Sprintf("file-%03d.txt", i)), "after\n")
	}
	reviews, complete, coverage, unswept := reconcileExecObservation(execObservation{
		Class: execScoped.String(), Roots: []string{workspace}, Files: capture.files, Omitted: capture.omitted,
	}, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" || len(reviews) != 12 {
		t.Fatalf("glob reviews=%d complete=%v coverage=%s unswept=%q", len(reviews), complete, coverage, unswept)
	}
}

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
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" || len(reviews) != 52 {
		t.Fatalf("Python writes=%d complete=%v coverage=%s unswept=%q", len(reviews), complete, coverage, unswept)
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
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" || len(reviews) != 512 {
		t.Fatalf("destination reviews=%d complete=%v coverage=%s unswept=%q", len(reviews), complete, coverage, unswept)
	}
}

func TestExecCaptureExpiredCandidatesDeduplicate(t *testing.T) {
	capture := newExecCapture(time.Now().Add(-time.Second))
	root := t.TempDir()
	for i := range 50000 {
		path := filepath.Join(root, fmt.Sprintf("candidate-%d", i))
		capture.add(path)
		capture.omit(path, "duplicate reason must not replace original")
	}
	if len(capture.files) != 0 || len(capture.omitted) != 50000 {
		t.Fatalf("expired capture: snapshots=%d omissions=%d", len(capture.files), len(capture.omitted))
	}
	for _, omission := range capture.omitted {
		if omission.Reason != "capture deadline" {
			t.Fatalf("lost first omission: %+v", omission)
		}
	}
}
