package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestManagedExecMChangesSurface(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const thread = "reviewer-thread"
	ctx, release, err := store.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	managedFiles := make([]mekugi.ReviewFile, 21)
	for i := range managedFiles {
		path := fmt.Sprintf("managed-%02d.txt", i+1)
		file := mekugi.RenderReviewFile("", path, "", "BODY SHOULD NOT LEAK\n")
		file.Origin = "formatter"
		managedFiles[i] = file
	}
	managedID := saveManagedExecChange(t, store, ctx, workspace, thread, "managed", managedFiles)
	mixedFiles := []mekugi.ReviewFile{
		func() mekugi.ReviewFile {
			file := mekugi.RenderReviewFile("", "managed-summary.txt", "", "managed\n")
			file.Origin = "generator"
			return file
		}(),
		mekugi.RenderReviewFile("", "direct-summary.txt", "", "direct\n"),
	}
	mixedID := saveManagedExecChange(t, store, ctx, workspace, thread, "mixed", mixedFiles)
	index, err := store.scoped(ctx).readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := listThreadChanges(index, thread)
	wantList := managedID + " managed only\n" + mixedID + "\n"
	if err != nil || stdout != wantList {
		t.Fatalf("managed list = %q, %v; want %q", stdout, err, wantList)
	}

	stdout, err = store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{managedID}})
	if err != nil || !strings.HasPrefix(stdout, managedID+" completed · exec formatter · generator, exact coverage\n") ||
		strings.Count(stdout, " · formatter\n") != 20 || !strings.Contains(stdout, "+1 more tool-managed files\n") ||
		strings.Contains(stdout, "managed-21.txt") || strings.Contains(stdout, "BODY SHOULD NOT LEAK") || strings.Contains(stdout, "@@") {
		t.Fatalf("default managed read = %q, %v", stdout, err)
	}

	stdout, err = store.readChanges(ctx, changeReadOptions{
		workspace: workspace, ids: []string{managedID}, paths: []string{"managed-21.txt"},
	})
	if err != nil || !strings.Contains(stdout, "managed-21.txt") ||
		!strings.Contains(stdout, "+BODY SHOULD NOT LEAK") || strings.Contains(stdout, "+1 more tool-managed files") {
		t.Fatalf("explicit managed path = %q, %v", stdout, err)
	}

	stdout, err = store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{managedID}, view: "history"})
	if err != nil || !strings.Contains(stdout, "exec_command input:\npython generate.py") ||
		!strings.Contains(stdout, "managed-21.txt") || !strings.Contains(stdout, "+BODY SHOULD NOT LEAK") {
		t.Fatalf("managed history = %q, %v", stdout, err)
	}

	stdout, err = store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{mixedID}, view: "summary"})
	if err != nil ||
		!strings.Contains(stdout, "1\t0\ttool-managed\tmanaged-summary.txt\n") ||
		!strings.Contains(stdout, "1\t0\tdirect-summary.txt\n") {
		t.Fatalf("managed summary labels = %q, %v", stdout, err)
	}

	index, err = store.scoped(ctx).readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	streamName, _, err := parseChangeID(managedID)
	if err != nil {
		t.Fatal(err)
	}
	stream := -1
	for i := range index.Streams {
		if changeStreamName(i) == streamName {
			stream = i
			break
		}
	}
	if stream < 0 {
		t.Fatalf("stream for %s was not retained", managedID)
	}
	data := newLiveDiffData()
	event := liveDiffChange{
		Workspace: workspace, Namespace: index.Namespace, Thread: thread, Stream: stream,
		ID: managedID, Change: index.Changes[managedID],
	}
	if err := data.apply(ctx, store, event); err != nil {
		t.Fatal(err)
	}
	files := data.files()
	if len(files) != 1 || len(files[0].Chunks) != 1 {
		t.Fatalf("saved DIFF groups managed effects into %d cards and %d chunks", len(files), func() int {
			if len(files) == 0 {
				return 0
			}
			return len(files[0].Chunks)
		}())
	}
	chunk := files[0].Chunks[0]
	if chunk.Review.Origin != "tool-managed" || !strings.Contains(chunk.Review.AfterPath, managedID+": 21 tool-managed files") ||
		strings.Count(chunk.Review.Incomplete, "Create \"") != 21 || !strings.Contains(chunk.Review.Incomplete, "managed-21.txt") ||
		strings.Contains(chunk.Review.Incomplete, "BODY SHOULD NOT LEAK") {
		t.Fatalf("saved DIFF managed card = %+v", chunk.Review)
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
		ExecutingThread: thread, CorrelationID: correlation, ChangeID: id, Applied: true,
		ReviewFiles: files, ExecOutcome: &execOutcome{
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
	want := "+ 5 tool-managed files (`generated-01.txt`, `generated-02.txt`, `generated-03.txt`, …)"
	if got != want {
		t.Fatalf("grouped receipt = %q, want %q", got, want)
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
