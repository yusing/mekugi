package router

import (
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
