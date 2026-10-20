package capture

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

func TestExecCaptureManyLiteralGlobFiles(t *testing.T) {
	workspace := t.TempDir()
	for i := range 512 {
		writeTestFile(t, filepath.Join(workspace, "many", fmt.Sprintf("file-%03d.txt", i)), "before\n")
	}
	capture := newExecCapture(time.Now().Add(10 * time.Second))
	capture.scopeRoots = []string{workspace}
	capture.entry(execscope.Entry{Kind: execscope.File, Operands: []execscope.Operand{{Path: filepath.Join(workspace, "many", "*.txt"), Glob: true}}})
	if len(capture.files) != 512 || len(capture.omitted) != 0 {
		t.Fatalf("glob baselines=%d omitted=%+v", len(capture.files), capture.omitted)
	}
	for i := range 12 {
		writeTestFile(t, filepath.Join(workspace, "many", fmt.Sprintf("file-%03d.txt", i)), "after\n")
	}
	result := Reconcile(t.Context(), Observation{
		Class: execscope.Scoped.String(), Roots: []string{workspace}, Files: capture.files, Omitted: capture.omitted,
	}, Comparison{})
	reviews, complete, coverage := result.Files, result.Complete, result.Coverage
	if !complete || coverage != CoverageExact || len(reviews) != 12 {
		t.Fatalf("glob reviews=%d complete=%v coverage=%s", len(reviews), complete, coverage)
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
