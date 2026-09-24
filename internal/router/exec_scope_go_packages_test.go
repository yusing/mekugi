package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func captureGoPackageTestObservation(t *testing.T, workspace, command string) *execObservation {
	t.Helper()
	observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false,
		execCaptureEnv{directory: workspace})
	if !observed || observation == nil || observation.Class != execScoped.String() || !observation.Sweep {
		t.Fatalf("package command %q capture = %+v observed=%v; want a swept scoped observation", command, observation, observed)
	}
	return observation
}

func packageSnapshotsByRelativePath(t *testing.T, workspace string, observation execObservation) map[string]execFileSnapshot {
	t.Helper()
	snapshots := make(map[string]execFileSnapshot, len(observation.Files))
	for _, snapshot := range observation.Files {
		relative, err := filepath.Rel(workspace, snapshot.Path)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[filepath.ToSlash(relative)] = snapshot
	}
	return snapshots
}

func TestRTKGoGeneratePackageCapturesTextAndBinaryWithOrigin(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "pkg")
	textPath, binaryPath := filepath.Join(pkg, "generate.go"), filepath.Join(pkg, "fixture.bin")
	writeTestFile(t, textPath, "package pkg\nconst Before = true\n")
	if err := os.WriteFile(binaryPath, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	observation := captureGoPackageTestObservation(t, workspace, "rtk go generate ./pkg")
	baselines := packageSnapshotsByRelativePath(t, workspace, *observation)
	text, textFound := baselines["pkg/generate.go"]
	binary, binaryFound := baselines["pkg/fixture.bin"]
	if len(baselines) != 2 || !textFound || text.Kind != execFileText || text.Content != "package pkg\nconst Before = true\n" || text.Origin != "go generate" {
		t.Fatalf("go generate text baseline = %+v; baselines=%+v", text, baselines)
	}
	if !binaryFound || binary.Kind != execFileBinary || binary.Hash == "" || binary.Origin != "go generate" {
		t.Fatalf("go generate binary baseline = %+v", binary)
	}

	writeTestFile(t, textPath, "package pkg\nconst After = true\n")
	if err := os.WriteFile(binaryPath, []byte{0, 1, 2, 4}, 0o600); err != nil {
		t.Fatal(err)
	}
	reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	byPath := make(map[string]mekugi.ReviewFile, len(reviews))
	for _, review := range reviews {
		byPath[filepath.Clean(reviewPath(review))] = review
	}
	if !complete || len(reviews) != 2 {
		t.Fatalf("go generate after-state reviews = %+v complete=%v", reviews, complete)
	}
	for path, want := range map[string]string{textPath: "-const Before = true\n+const After = true\n", binaryPath: "Binary files"} {
		review, found := byPath[filepath.Clean(path)]
		if !found || review.Origin != "go generate" || !strings.Contains(review.Diff, want) {
			t.Errorf("go generate review %q = %+v found=%v; want origin and %q", path, review, found, want)
		}
	}
}

func TestGoTestPackageOperandPositionsCaptureArtifactsAndLeaveOutsidePartial(t *testing.T) {
	for _, command := range []string{"go test -run PAT ./pkg", "go test ./pkg -run PAT"} {
		t.Run(strings.ReplaceAll(command, " ", "_"), func(t *testing.T) {
			workspace := t.TempDir()
			pkg := filepath.Join(workspace, "pkg")
			artifact := filepath.Join(pkg, "test-artifact.txt")
			writeTestFile(t, filepath.Join(pkg, "pkg_test.go"), "package pkg\n")
			writeTestFile(t, filepath.Join(pkg, "nested", "input.txt"), "nested package data\n")
			writeTestFile(t, filepath.Join(pkg, "node_modules", "dep", "index.js"), "dependency\n")
			writeTestFile(t, filepath.Join(pkg, ".git", "objects", "object"), "git data\n")
			writeTestFile(t, artifact, "before\n")

			observation := captureGoPackageTestObservation(t, workspace, command)
			baselines := packageSnapshotsByRelativePath(t, workspace, *observation)
			baseline, found := baselines["pkg/test-artifact.txt"]
			if !found || baseline.Content != "before\n" || baseline.Origin != "go test" {
				t.Fatalf("test artifact baseline = %+v found=%v; files=%+v", baseline, found, baselines)
			}
			if _, found := baselines["pkg/nested/input.txt"]; !found {
				t.Fatalf("nested package file missing from bounded baseline: %+v", baselines)
			}
			for relative := range baselines {
				if strings.HasPrefix(relative, "pkg/node_modules/") || strings.HasPrefix(relative, "pkg/.git/") {
					t.Fatalf("dependency/VCS contents entered package baseline: %s", relative)
				}
			}
			unchanged, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
			if !complete || coverage != execCoverageExact || len(unchanged) != 0 {
				t.Fatalf("unchanged package snapshots = %+v complete=%v coverage=%q", unchanged, complete, coverage)
			}

			writeTestFile(t, artifact, "after\n")
			outside := filepath.Join(workspace, "outside-package-artifact.txt")
			writeTestFile(t, outside, "not baselined\n")
			reviews, _, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
			var packageReview, outsideReview bool
			for _, review := range reviews {
				switch filepath.Clean(reviewPath(review)) {
				case filepath.Clean(artifact):
					packageReview = review.Origin == "go test" && strings.Contains(review.Diff, "-before\n+after\n")
				case filepath.Clean(outside):
					outsideReview = true
				}
			}
			if coverage != execCoveragePartial || !packageReview || !outsideReview {
				t.Fatalf("package/outside artifact reviews = %+v coverage=%q; want managed package baseline and partial outside finding", reviews, coverage)
			}
		})
	}
}

func TestGoPackageScopeCapAndImportOnlyCommandsStayConservative(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "pkg")
	for i := range maxExecCaptureFiles + 1 {
		name := filepath.Join(pkg, "file-"+strconv.Itoa(i)+".txt")
		writeTestFile(t, name, "content\n")
	}
	result := execGoPackageScope(execProviderInput{
		identity: "go", args: []string{"test", "./pkg"}, cwd: workspace, deadline: time.Now().Add(5 * time.Second),
	})
	if len(result.scope) != 1 || len(result.scope[0].Operands) != maxExecCaptureFiles || !result.open || result.reason == "" {
		t.Fatalf("capped package scope = %+v; want bounded open result", result)
	}

	for _, command := range []string{"go test example.com/foreign/pkg", "go generate", `go test "$PACKAGE"`} {
		plan := classifyExecShell(command, workspace, "bash")
		if plan.Class != execOpaque {
			t.Errorf("package command without a literal local directory %q class=%v reason=%q, want opaque", command, plan.Class, plan.Reason)
		}
	}
}

func reviewPath(review mekugi.ReviewFile) string {
	if review.AfterPath != "" {
		return review.AfterPath
	}
	return review.BeforePath
}

// Unread managed package files are hints, not observed edits. If they later
// change, the sweep must still find them without claiming a fabricated diff.
func TestManagedPackageCaptureCapLeavesSweepUnclaimed(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "pkg")
	paths := make([]string, maxExecCaptureFiles+8)
	for i := range paths {
		paths[i] = filepath.Join(pkg, fmt.Sprintf("source-%03d.go", i))
		writeTestFile(t, paths[i], "package pkg\n")
	}
	// A broad sweep ignores this path; the deferred hint must still detect it.
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "pkg/source-263.go\n")
	start := execWindowStart(os.TempDir())
	capture := newExecCapture(time.Now().Add(5 * time.Second))
	capture.sweepRoots = []string{workspace}
	entry := execProviderFiles(paths, false)
	entry.Origin = "go test"
	capture.entry(entry)
	observation := execObservation{Files: capture.files, Omitted: capture.omitted, Roots: []string{workspace}, Sweep: true, WindowStart: start, Class: execScoped.String()}
	if len(observation.Files) != maxExecCaptureFiles || len(observation.Omitted) != 8 || !observation.Omitted[0].Deferred {
		t.Fatalf("bounded package hints: captured=%d omitted=%+v", len(observation.Files), observation.Omitted)
	}
	unchanged, complete, coverage, _ := reconcileExecObservation(observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(unchanged) != 0 {
		t.Fatalf("unchanged package produced evidence: %+v %v %s", unchanged, complete, coverage)
	}
	outside := paths[len(paths)-1]
	writeTestFile(t, outside, "package pkg\n// changed\n")
	reviews, complete, coverage, _ := reconcileExecObservation(observation, execReconcileEnv{})
	if !complete || coverage != execCoveragePartial || len(reviews) != 1 || reviewPath(reviews[0]) != outside || reviews[0].Incomplete == "" || reviews[0].Origin != "go test" {
		t.Fatalf("unbaselined package write was not checked honestly: %+v %v %s", reviews, complete, coverage)
	}
}

func TestGoFixPackageScopeCapturesOnlyGoSources(t *testing.T) {
	workspace := t.TempDir()
	goFile := filepath.Join(workspace, "pkg", "source.go")
	textFile := filepath.Join(workspace, "pkg", "notes.txt")
	writeTestFile(t, goFile, "package pkg\n")
	writeTestFile(t, textFile, "notes\n")
	observation := captureGoPackageTestObservation(t, workspace, "go fix ./pkg")
	if len(observation.Files) != 1 || observation.Files[0].Path != goFile || observation.Files[0].Origin != "go fix" {
		t.Fatalf("go fix baselines: %+v", observation.Files)
	}
	writeTestFile(t, goFile, "package pkg\n// fixed\n")
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 1 || reviews[0].Origin != "go fix" || !strings.Contains(reviews[0].Diff, "+// fixed") {
		t.Fatalf("go fix review: %+v complete=%v coverage=%s", reviews, complete, coverage)
	}
}

func TestManagedFormatterDeferredHintsStayInsideSweepRoot(t *testing.T) {
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	inside := filepath.Join(workspace, "format.go")
	outside := filepath.Join(elsewhere, "format.go")
	writeTestFile(t, inside, "package p\n")
	writeTestFile(t, outside, "package p\n")
	capture := newExecCapture(time.Now().Add(time.Second))
	capture.sweepRoots = []string{workspace}
	capture.origin = "gofmt"
	capture.files = make([]execFileSnapshot, maxExecCaptureFiles)
	capture.add(inside)
	capture.add(outside)
	if len(capture.omitted) != 2 || !capture.omitted[0].Deferred || capture.omitted[1].Deferred {
		t.Fatalf("formatter hints claimed paths outside sweep: %+v", capture.omitted)
	}
}

func TestManagedDeferredHintWithUnavailableClockStaysIncomplete(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "pkg", "source.go")
	writeTestFile(t, path, "package pkg\n")
	observation := execObservation{
		Class: execScoped.String(), Roots: []string{workspace}, Sweep: true,
		Omitted: []execOmission{{Path: path, Origin: "go test", Reason: "capture limit", Deferred: true}},
		// No comparable window start models an unavailable filesystem clock.
	}
	reviews, complete, coverage, unswept := reconcileExecObservation(observation, execReconcileEnv{})
	if complete || coverage != execCoverageUnswept || unswept == "" || len(reviews) != 1 || reviews[0].Origin != "go test" ||
		!strings.Contains(reviews[0].Incomplete, "clock incomparable") || strings.Contains(reviews[0].Diff, "+package pkg") {
		t.Fatalf("incomparable clock hid or invented managed evidence: %+v complete=%v coverage=%s unswept=%q", reviews, complete, coverage, unswept)
	}
}
