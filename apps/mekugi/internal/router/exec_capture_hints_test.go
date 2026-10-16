package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

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
	observation := execObservation{
		Class: execscope.Scoped.String(), Roots: []string{workspace},
		Omitted: []execOmission{{Path: path, Origin: "gofmt", Reason: "capture deadline", Deferred: true}},
	}
	reviews, complete, _, unswept := reconcileExecObservation(observation, execReconcileEnv{})
	if complete || unswept != "" || len(reviews) != 1 || reviews[0].Incomplete == "" || strings.Contains(reviews[0].Diff, "+package pkg") {
		t.Fatalf("unavailable formatter baseline invented evidence: %+v complete=%v unswept=%q", reviews, complete, unswept)
	}
}
