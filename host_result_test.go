package mekugi

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func assertNoHostSuccessProjection(t *testing.T, result HostTranslation) {
	t.Helper()
	if result.Report != "" || len(result.TargetAliases) != 0 || len(result.Patch) != 0 || result.PatchSummary != (HostPatchSummary{}) {
		t.Fatalf("failure published successful projection: %+v", result)
	}
}

func TestFailedTranslationWithholdsSuccessProjection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "old\r\n", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "type " + row(1, "old") + ` "old\n"`}}
	result, err := TranslateForHostAt(t.Context(), root, edits, "")
	if err == nil || !strings.Contains(err.Error(), "produced no header") {
		t.Fatalf("translation error = %v", err)
	}
	assertNoHostSuccessProjection(t, result)
	if result.Outcome != (HostOutcome{Stage: "translated", Status: "failed"}) || result.Change.Files != 1 || len(result.Failures) == 0 {
		t.Fatalf("failure metadata = %+v", result)
	}
	if got := readTestFile(t, root, "file.txt"); got != "old\r\n" {
		t.Fatalf("translation mutated content: %q", got)
	}
}

func TestHostStagingFailureWithholdsSuccessProjection(t *testing.T) {
	root := t.TempDir()
	// The baseline name fits NAME_MAX; its backup reservation name does not.
	path := strings.Repeat("x", 240) + ".txt"
	writeTestFile(t, root, path, "old\n", 0o644)
	capability, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	edits := []FileEdit{{Path: path, Script: "type " + row(1, "old") + ` "new"`}}
	result, err := ApplyForHost(t.Context(), Workspace{Root: capability}, edits, "")
	if err == nil || !strings.Contains(err.Error(), "creating backup reservation") {
		t.Fatalf("staging error = %v", err)
	}
	assertNoHostSuccessProjection(t, result)
	if result.Outcome != (HostOutcome{Stage: "applied", Status: "failed"}) || result.Change.Applied {
		t.Fatalf("staging outcome = %+v", result)
	}
	if got := readTestFile(t, root, path); got != "old\n" {
		t.Fatalf("staging failure changed content: %q", got)
	}
}

func TestHostCommitFinalizationWithholdsSuccessProjection(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback succeeds", true: "rollback fails"}[rollbackFails], func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "a.txt", "a\n", 0o644)
			writeTestFile(t, root, "b.txt", "b\n", 0o644)
			edits := []FileEdit{{Path: "a.txt", Script: "type " + row(1, "a") + ` "A"`}, {Path: "b.txt", Script: "type " + row(1, "b") + ` "B"`}}
			changes, filesystem, report, aliases, err := evaluateScriptAt(t.Context(), root, edits)
			if err != nil {
				t.Fatal(err)
			}
			operations := &failingFileOperations{fileOperations: hostFileOperations{filesystem: filesystem}, failRename: map[int]error{4: errors.New("injected install failure")}}
			if rollbackFails {
				operations.failRename[5] = errors.New("injected restore failure")
			}
			commitErr := commitChanges(changes, operations)
			if commitErr == nil {
				t.Fatal("injected commit succeeded")
			}
			result, err := finishHostChange(t.Context(), "", joinedFileEditScripts(edits), hostTranslationResult(changes, report, aliases, true), "applied", commitErr, true)
			if !errors.Is(err, commitErr) || len(result.Failures) == 0 || result.Outcome != (HostOutcome{Stage: "applied", Status: "failed"}) {
				t.Fatalf("finalization error %v, result %+v", err, result)
			}
			assertNoHostSuccessProjection(t, result)
		})
	}
}

func TestHostLateCancellationRetainsEffectMetadata(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "translated", true: "applied"}[applied], func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "a.txt", "a\n", 0o644)
			edits := []FileEdit{{Path: "a.txt", Script: "type " + row(1, "a") + ` "A"`}}
			changes, filesystem, report, aliases, err := evaluateScriptAt(t.Context(), root, edits)
			if err != nil {
				t.Fatal(err)
			}
			pending := hostTranslationResult(changes, report, aliases, true)
			if applied {
				err = commitChanges(changes, hostFileOperations{filesystem: filesystem})
			} else {
				err = translateHostResult(t.Context(), changes, &pending)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			result, err := finishHostChange(ctx, "", joinedFileEditScripts(edits), pending, "", nil, applied)
			if !errors.Is(err, context.Canceled) || result.Change.Applied != applied || result.Outcome.Status != "succeeded" {
				t.Fatalf("late cancellation error %v, result %+v", err, result)
			}
			assertNoHostSuccessProjection(t, result)
			want := "a\n"
			if applied {
				want = "A\n"
			}
			if got := readTestFile(t, root, "a.txt"); got != want {
				t.Fatalf("late cancellation content = %q, want %q", got, want)
			}
		})
	}
}

func TestHostSuccessPublishesPreparedProjection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "old\n", 0o644)
	edits := []FileEdit{{Path: "file.txt", Script: "type " + row(1, "old") + ` "new"`}}
	translated, err := TranslateForHostAt(t.Context(), root, edits, "")
	if err != nil || translated.Report == "" || len(translated.TargetAliases) != 1 || len(translated.Patch) == 0 ||
		translated.PatchSummary != (HostPatchSummary{Files: 1, Bytes: len(translated.Patch)}) {
		t.Fatalf("translation error %v, projection %+v", err, translated)
	}
	capability, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	applied, err := ApplyForHostAt(t.Context(), root, edits, "")
	if err != nil || applied.Report != translated.Report || len(applied.TargetAliases) != 1 ||
		applied.TargetAliases[0] != translated.TargetAliases[0] || !applied.Change.Applied || len(applied.Patch) != 0 {
		t.Fatalf("application error %v, projection %+v", err, applied)
	}
	unchanged, err := TranslateForHostAt(t.Context(), root, []FileEdit{{Path: "file.txt", Script: ""}}, "")
	if err != nil || unchanged.Report == "" || !unchanged.Change.AlreadySatisfied || len(unchanged.Patch) != 0 || len(unchanged.TargetAliases) != 0 {
		t.Fatalf("no-op error %v, projection %+v", err, unchanged)
	}
}

func TestPublicAPIsRejectNilContext(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "old\n", 0o644)
	capability, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	edits := []FileEdit{{Path: "file.txt", Script: "type " + row(1, "old") + ` "new"`}}
	for _, test := range []struct {
		name string
		call func() (HostTranslation, error)
	}{
		{"Apply", func() (HostTranslation, error) {
			return HostTranslation{}, Apply(nil, Workspace{Root: capability}, edits)
		}},
		{"ApplyForHost", func() (HostTranslation, error) {
			return ApplyForHost(nil, Workspace{Root: capability}, edits, "")
		}},
		{"ApplyForHostRoot", func() (HostTranslation, error) { return ApplyForHostRoot(nil, capability, edits, "") }},
		{"TranslateForHostAt", func() (HostTranslation, error) { return TranslateForHostAt(nil, root, edits, "") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if panicValue := recover(); panicValue != nil {
					t.Errorf("nil context caused panic: %v", panicValue)
				}
				if got := readTestFile(t, root, "file.txt"); got != "old\n" {
					t.Errorf("nil context mutated content: %q", got)
				}
			}()
			result, err := test.call()
			if err == nil || err.Error() != "context is nil" || !reflect.DeepEqual(result, HostTranslation{}) {
				t.Fatalf("result %+v, error %v; want zero result and context is nil", result, err)
			}
		})
	}
}
