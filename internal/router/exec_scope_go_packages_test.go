package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoTestAndGenerateDoNotBaselineWorkspace(t *testing.T) {
	for _, command := range []string{"go test ./pkg", "go generate ./pkg", "python script.py"} {
		t.Run(command, func(t *testing.T) {
			workspace := t.TempDir()
			packageFile := filepath.Join(workspace, "pkg", "source.go")
			artifact := filepath.Join(workspace, "pkg", "generated.bin")
			external := filepath.Join(workspace, "hook-like.txt")
			writeTestFile(t, packageFile, "package pkg\n")
			writeTestFile(t, artifact, "before\n")
			observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
			if !observed || observation == nil {
				t.Fatalf("%q observation = %+v observed=%v", command, observation, observed)
			}
			if len(observation.Files) != 0 {
				t.Fatalf("%q baselined undeclared package contents: %+v", command, observation.Files)
			}
			writeTestFile(t, packageFile, "package pkg\n// hook\n")
			writeTestFile(t, artifact, "after\n")
			writeTestFile(t, external, "external edit\n")
			reviews, _, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
			if len(reviews) != 0 {
				t.Fatalf("%q attributed undeclared writes: %+v", command, reviews)
			}
		})
	}
}

func TestGoFixPackageScopeCapturesOnlyGoSources(t *testing.T) {
	workspace := t.TempDir()
	goFile := filepath.Join(workspace, "pkg", "source.go")
	textFile := filepath.Join(workspace, "pkg", "notes.txt")
	external := filepath.Join(workspace, "hook-like.txt")
	writeTestFile(t, goFile, "package pkg\n")
	writeTestFile(t, textFile, "notes\n")
	observation, observed := captureExecObservation([]execCommandInput{{Command: "go fix ./pkg", Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
	if !observed || observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != goFile || observation.Files[0].Origin != "go fix" {
		t.Fatalf("go fix explicit package source baseline: %+v observed=%v", observation, observed)
	}
	writeTestFile(t, goFile, "package pkg\n// fixed\n")
	writeTestFile(t, textFile, "changed\n")
	writeTestFile(t, external, "hook-like\n")
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" || len(reviews) != 1 || reviews[0].Origin != "go fix" || !strings.Contains(reviews[0].Diff, "+// fixed") {
		t.Fatalf("go fix review: %+v complete=%v coverage=%s unswept=%q", reviews, complete, coverage, unswept)
	}
}

func TestFormatterDeferredHintsStayInsideExplicitScope(t *testing.T) {
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	inside := filepath.Join(workspace, "format.go")
	outside := filepath.Join(elsewhere, "format.go")
	writeTestFile(t, inside, "package p\n")
	writeTestFile(t, outside, "package p\n")
	capture := newExecCapture(time.Now().Add(time.Second))
	capture.scopeRoots = []string{workspace}
	capture.origin = "gofmt"
	capture.files = make([]execFileSnapshot, maxExecCaptureFiles)
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
	observation := execObservation{
		Class: execScoped.String(), Roots: []string{workspace},
		Omitted: []execOmission{{Path: path, Origin: "gofmt", Reason: "capture limit", Deferred: true}},
	}
	reviews, complete, _, unswept := reconcileExecObservation(observation, execReconcileEnv{})
	if complete || unswept != "" || len(reviews) != 1 || reviews[0].Incomplete == "" || strings.Contains(reviews[0].Diff, "+package pkg") {
		t.Fatalf("unavailable formatter baseline invented evidence: %+v complete=%v unswept=%q", reviews, complete, unswept)
	}
}

func TestGoFixPackageScopeCapIsBounded(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "pkg")
	for i := range maxExecCaptureFiles + 1 {
		writeTestFile(t, filepath.Join(pkg, fmt.Sprintf("source-%03d.go", i)), "package pkg\n")
	}
	result := execGoPackageScope(execProviderInput{identity: "go", args: []string{"fix", "./pkg"}, cwd: workspace, deadline: time.Now().Add(5 * time.Second)})
	if len(result.scope) != 1 || len(result.scope[0].Operands) != maxExecCaptureFiles || !result.open || result.reason == "" {
		t.Fatalf("capped go fix package source scope = %+v", result)
	}
}
