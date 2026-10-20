package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func TestManagedExecMChangesSurface(t *testing.T) {
	f := newMChangesSliceFixture(t, "managed-history")
	managed := mekugi.RenderReviewFile("", "managed.txt", "", "generated body\n")
	managed.Origin = "generator"
	id := saveManagedExecChange(t, f.store, f.ctx, f.workspace, f.thread, "managed", []mekugi.ReviewFile{managed})
	direct := mekugi.RenderReviewFile("", "direct.txt", "", "authored body\n")
	mixed := saveManagedExecChange(t, f.store, f.ctx, f.workspace, f.thread, "mixed", []mekugi.ReviewFile{managed, direct})
	list, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{id, mixed}, view: "list"})
	if err != nil || list != mixed+" +1 -0\n" {
		t.Fatalf("authored list: %q, %v", list, err)
	}
	for _, mode := range []string{"", "summary", "net"} {
		text, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{id, mixed}, view: mode})
		if err != nil || strings.Contains(text, "generated") || strings.Contains(text, "managed") || !strings.Contains(text, "direct.txt") {
			t.Fatalf("managed noise in %s: %q, %v", mode, text, err)
		}
	}
	text, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{id}, view: "history"})
	if err != nil || !strings.Contains(text, "generated body") {
		t.Fatalf("diagnostic history lost: %q, %v", text, err)
	}
	data, err := f.store.liveDiffSnapshot(f.ctx, liveDiffScope{Workspaces: map[string]map[string]bool{f.workspace: {f.thread: true}}})
	if err != nil || len(data.files()) != 1 || !strings.HasSuffix(data.files()[0].Path, "direct.txt") {
		t.Fatalf("managed saved Diff: %+v, %v", data.files(), err)
	}
}

func saveManagedExecChange(
	t *testing.T,
	store *mekugiReplayStore,
	ctx context.Context,
	workspace, thread, correlation string,
	files []mekugi.ReviewFile,
) string {
	t.Helper()
	id, err := store.reserveChange(ctx, workspace, thread, correlation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{correlation: {
		ToolName: nativeExecCommandToolName, Script: "python generate.py", Root: workspace,
		ExecutingThread: thread, CorrelationID: correlation, ChangeID: id, ReviewFiles: files, ExecOutcome: &execOutcome{
			Status: execStatusCompleted, Class: "generator", Labels: []string{"formatter"}, Coverage: execCoverageExact,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestManagedExecReceiptGroupsFiles(t *testing.T) {
	workspace := t.TempDir()
	files := make([]mekugi.ReviewFile, 5)
	for i := range files {
		path := fmt.Sprintf("generated-%02d.txt", i+1)
		files[i] = mekugi.RenderReviewFile("", path, "", "content\n")
		files[i].Origin = "generator"
	}
	got := editReceiptText(workspace, mekugiHistory{ReviewFiles: files})
	want := ""
	if got != want {
		t.Fatalf("grouped receipt = %q, want %q", got, want)
	}
}

func TestExecCaptureNamesEverySharedOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	writeTestFile(t, path, "package a\n")
	capture := newExecCapture(time.Now().Add(time.Minute))
	for _, origin := range []string{"", "gofmt", "goimports", "gofmt"} {
		capture.origin = origin
		capture.add(path)
	}
	if len(capture.files) != 1 || capture.files[0].Origin != "" || capture.files[0].AlsoManaged != "gofmt, goimports" {
		t.Fatalf("shared capture = %+v, want one direct file also managed by gofmt, goimports", capture.files)
	}
}

func TestExecReceiptOmitsSharedOriginNamedByLabels(t *testing.T) {
	workspace := t.TempDir()
	file := mekugi.RenderReviewFile("a.go", "a.go", "package a\n\nfunc A() {}\n", "package a\n")
	file.OriginNote = "goimports" + sharedOriginSuffix
	for _, test := range []struct {
		labels []string
		note   bool
	}{
		{labels: []string{"python3", "goimports"}},
		{labels: []string{"python3"}, note: true},
		{note: true},
	} {
		got := editReceiptText(workspace, mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}, ExecOutcome: &execOutcome{Labels: test.labels}})
		if strings.Contains(got, "(goimports also ran)") != test.note {
			t.Errorf("labels %q receipt = %q, want note %v", test.labels, got, test.note)
		}
	}
}

func TestMergedExecObservationReconcilesDuplicatePathsAndExcludesSameCellPatch(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "shared.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var budget = maxExecContentBytes
	before := snapshotExecFile(path, &budget)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	members := []execCompletion{
		{history: mekugiHistory{ExecObservation: &execObservation{Files: []execFileSnapshot{before}}}, callID: "first"},
		{history: mekugiHistory{ExecObservation: &execObservation{Files: []execFileSnapshot{before}}}, callID: "second"},
	}
	merged := mergeExecObservations(members)
	reviews, complete, coverage, _ := reconcileExecObservation(merged, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 1 || reviews[0].BeforePath != path || reviews[0].AfterPath != path {
		t.Fatalf("duplicate-path reconciliation = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}

	excludedMembers := []execCompletion{
		{history: mekugiHistory{ExecObservation: &execObservation{Files: []execFileSnapshot{before}}}, callID: "command"},
		{history: mekugiHistory{ExecObservation: &execObservation{Excluded: []string{path}}}, callID: "patch"},
	}
	excluded := mergeExecObservations(excludedMembers)
	reviews, complete, coverage, _ = reconcileExecObservation(excluded, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 0 {
		t.Fatalf("same-cell patch exclusion = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}
}
