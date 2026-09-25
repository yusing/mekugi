package router

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/syntax"
)

func putTestChange(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace, correlation string, files ...mekugi.ReviewFile) string {
	t.Helper()
	id, err := store.reserveChange(ctx, workspace, "stock-thread", correlation)
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: correlation, ReviewFiles: files}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{correlation: history}); err != nil {
		t.Fatal(err)
	}
	return id
}

func mutateTestChanges(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace string, arguments ...string) (string, int) {
	t.Helper()
	options, err := parseChangeRead(arguments, workspace)
	if err != nil {
		t.Fatal(err)
	}
	output, status, err := store.mutateChanges(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	return output, status
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMChangesRevertAndApplyReportHistoryStat(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	original := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"
	edited := strings.Replace(original, "two\n", "TWO\n", 1)
	later := strings.Replace(edited, "seven\n", "SEVEN\n", 1)
	writeTestFile(t, filepath.Join(workspace, "a.txt"), later)
	writeTestFile(t, filepath.Join(workspace, "moved.txt"), "kept\n")
	first := putTestChange(t, ctx, store, workspace, "first",
		mekugi.RenderReviewFile("a.txt", "a.txt", original, edited),
		mekugi.RenderReviewFile("", "new.txt", "", "created\n"))
	second := putTestChange(t, ctx, store, workspace, "second",
		mekugi.RenderReviewFile("a.txt", "a.txt", edited, later),
		mekugi.RenderReviewFile("gone.txt", "", "removed\n", ""),
		mekugi.RenderReviewFile("old.txt", "moved.txt", "", ""))
	writeTestFile(t, filepath.Join(workspace, "new.txt"), "created\n")

	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", second)
	want := "undo: mchanges apply " + second + "\n" +
		" M a.txt +1 -1\n" +
		"   gone.txt clean\n" +
		"   old.txt clean\n"
	if status != 0 || output != want {
		t.Fatalf("revert second = %d %q, want %q", status, output, want)
	}
	if got := readTestFile(t, filepath.Join(workspace, "a.txt")); got != edited {
		t.Fatalf("a.txt = %q", got)
	}
	if readTestFile(t, filepath.Join(workspace, "gone.txt")) != "removed\n" || readTestFile(t, filepath.Join(workspace, "old.txt")) != "kept\n" {
		t.Fatal("deletion or move was not reverted")
	}
	if _, err := os.Lstat(filepath.Join(workspace, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("move destination remains: %v", err)
	}
	// The router records each mutation from its captured scope.
	putTestChange(t, ctx, store, workspace, "revert-second",
		mekugi.RenderReviewFile("a.txt", "a.txt", later, edited),
		mekugi.RenderReviewFile("", "gone.txt", "", "removed\n"),
		mekugi.RenderReviewFile("moved.txt", "old.txt", "", ""))

	output, status = mutateTestChanges(t, ctx, store, workspace, "revert", first+".."+second)
	want = "undo: revert this command's change (newest in mchanges --list)\n" +
		"   a.txt clean · 1 hunk already reverted\n" +
		"   gone.txt clean · 1 hunk already reverted\n" +
		"   new.txt clean\n" +
		"   old.txt clean · move already reverted\n"
	if status != 0 || output != want {
		t.Fatalf("revert range = %d %q, want %q", status, output, want)
	}
	if got := readTestFile(t, filepath.Join(workspace, "a.txt")); got != original {
		t.Fatalf("a.txt = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("created file remains: %v", err)
	}
	putTestChange(t, ctx, store, workspace, "revert-range",
		mekugi.RenderReviewFile("a.txt", "a.txt", edited, original),
		mekugi.RenderReviewFile("new.txt", "", "created\n", ""))

	output, status = mutateTestChanges(t, ctx, store, workspace, "apply", first, "--", "a.txt")
	if status != 0 || output != "undo: mchanges revert "+first+" -- a.txt\n M a.txt +1 -1\n" {
		t.Fatalf("apply first = %d %q", status, output)
	}
	if got := readTestFile(t, filepath.Join(workspace, "a.txt")); got != edited {
		t.Fatalf("a.txt = %q", got)
	}
	putTestChange(t, ctx, store, workspace, "apply-first", mekugi.RenderReviewFile("a.txt", "a.txt", original, edited))
	output, status = mutateTestChanges(t, ctx, store, workspace, "apply", first, "--", "a.txt")
	if status != 0 || output != "nothing changed\n M a.txt +1 -1 · 1 hunk already applied\n" {
		t.Fatalf("repeated apply = %d %q", status, output)
	}
}

func TestMChangesRevertLeavesConflictMarkers(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	original := "one\ntwo\nthree\nfour\nfive\n"
	edited := "one\ntwo\nagent\nfour\nfive\n"
	id := putTestChange(t, ctx, store, workspace, "edit",
		mekugi.RenderReviewFile("a.txt", "a.txt", original, edited),
		mekugi.RenderReviewFile("", "b.txt", "", "created\n"))
	writeTestFile(t, filepath.Join(workspace, "a.txt"), "one\ntwo\nuser\nfour\nfive\n")
	writeTestFile(t, filepath.Join(workspace, "b.txt"), "created\nextra\n")
	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", id)
	if status != 1 || !strings.HasPrefix(output, "conflicts in 2 files: ") || !strings.Contains(output, "\nUU a.txt stat unavailable: file changed outside mchanges history · 1 conflict\n") ||
		!strings.Contains(output, "UU b.txt ") || !strings.Contains(output, "kept: content outside the recorded change remains") {
		t.Fatalf("conflict revert = %d %q", status, output)
	}
	want := "one\ntwo\n<<<<<<< workspace\nuser\n=======\nthree\n>>>>>>> mchanges revert " + id + "\nfour\nfive\n"
	if got := readTestFile(t, filepath.Join(workspace, "a.txt")); got != want {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readTestFile(t, filepath.Join(workspace, "b.txt")); got != "extra\n" {
		t.Fatalf("b.txt = %q", got)
	}
}

func TestMChangesRevertParsing(t *testing.T) {
	workspace := t.TempDir()
	for _, arguments := range [][]string{{"revert"}, {"apply", "--summary", "amber1"}, {"revert", "--list"}} {
		if _, err := parseChangeRead(arguments, workspace); err == nil {
			t.Fatalf("%q parsed", arguments)
		}
	}
	options, err := parseChangeRead([]string{"revert", "amber1..amber2", "--", "a.txt"}, workspace)
	if err != nil || options.view != "revert" || len(options.ids) != 2 || options.paths[0] != "a.txt" {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

// The router observes mchanges revert as a declared writer, so the revert is a
// recorded change of its own and reverting it restores the reverted edit.
func TestMChangesRevertIsRecordedAndRevertable(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "a.txt")
	writeTestFile(t, target, "one\ntwo\nthree\n")

	run := func(callID, command string, effect func(ctx context.Context)) mekugiHistory {
		t.Helper()
		transform := prepareNativeStockTransform(t, proxy, workspace, "exec-session-"+callID)
		arguments := string(mustMarshalJSON(map[string]any{"cmd": command, "workdir": workspace}))
		streamNativeExecCommand(t, transform, callID, arguments)
		observation := transform.local[callID].ExecObservation
		if observation == nil || observation.Class != "declared" {
			t.Fatalf("%q observation = %+v", command, observation)
		}
		effect(transform.ctx)
		reconcileExecItems(t, proxy, workspace, []any{
			map[string]any{"type": "function_call", "call_id": callID, "name": nativeExecCommandToolName, "arguments": arguments},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": nativeExecOutput("Process exited with code 0")},
		})
		history, found, err := proxy.replayStore.lookup(t.Context(), workspace, callID+":exec:1")
		if err != nil || !found || history.ChangeID == "" {
			t.Fatalf("%q record = %+v found=%v err=%v", command, history, found, err)
		}
		return history
	}
	edit := run("edit-call", "sed -i s/two/TWO/ a.txt", func(context.Context) {
		writeTestFile(t, target, "one\nTWO\nthree\n")
	})
	var output string
	revert := run("revert-call", "mchanges revert "+edit.ChangeID, func(ctx context.Context) {
		var status int
		output, status = mutateTestChanges(t, ctx, proxy.replayStore, workspace, "revert", edit.ChangeID)
		if status != 0 {
			t.Fatalf("revert status %d: %q", status, output)
		}
	})
	if output != "undo: mchanges apply "+edit.ChangeID+"\n   a.txt clean\n" || readTestFile(t, target) != "one\ntwo\nthree\n" {
		t.Fatalf("revert output %q", output)
	}
	if revert.ChangeID == edit.ChangeID || len(revert.ReviewFiles) != 1 || !strings.Contains(revert.ReviewFiles[0].Diff, "-TWO\n+two\n") {
		t.Fatalf("revert record = %+v", revert)
	}
	run("undo-call", "mchanges revert "+revert.ChangeID, func(ctx context.Context) {
		var status int
		output, status = mutateTestChanges(t, ctx, proxy.replayStore, workspace, "revert", revert.ChangeID)
		if status != 0 {
			t.Fatalf("undo status %d: %q", status, output)
		}
	})
	if output != "undo: mchanges apply "+revert.ChangeID+"\n M a.txt +1 -1\n" || readTestFile(t, target) != "one\nTWO\nthree\n" {
		t.Fatalf("undo output %q, file %q", output, readTestFile(t, target))
	}
}

func TestMChangesMutationClassification(t *testing.T) {
	workspace := t.TempDir()
	resolver := func(options changeReadOptions, _ time.Time) ([]string, error) {
		if options.view != "revert" || options.workspace != workspace || len(options.ids) != 1 {
			t.Fatalf("resolver options = %+v", options)
		}
		return []string{filepath.Join(workspace, "a.txt")}, nil
	}
	classify := func(command string, changes execChangeResolver) execPlan {
		return classifyExecShellWithin(command, workspace, "bash", time.Now().Add(execProviderBudget), 0, changes)
	}
	if plan := classify("mchanges --list && mchanges amber1 --summary", resolver); plan.Class != execNeutral {
		t.Fatalf("read plan = %+v", plan)
	}
	for _, command := range []string{"mchanges revert amber1", "mchanges revert amber1 2>&1 | tail -n 50", "(mchanges revert amber1)"} {
		plan := classify(command, resolver)
		if plan.Class != execDeclared || !slices.Equal(plan.Labels, []string{"mchanges revert"}) ||
			!slices.Equal(execPlanScope(plan, workspace), []string{"file:a.txt"}) {
			t.Fatalf("%q plan = %+v", command, plan)
		}
	}
	if plan := classify("mchanges revert amber1", nil); plan.Class != execOpaque {
		t.Fatalf("unresolved plan = %+v", plan)
	}
	// Only a dynamic subcommand can hide a mutation.
	if plan := classify(`mchanges amber1 --summary -- "$f"`, resolver); plan.Class != execNeutral {
		t.Fatalf("dynamic read plan = %+v", plan)
	}
	for _, command := range []string{`mchanges "$sub" amber1`, `mchanges revert "$id"`} {
		if plan := classify(command, resolver); plan.Class != execOpaque {
			t.Fatalf("%q plan = %+v", command, plan)
		}
	}
}

func TestMChangesRevertReportsUnknownStat(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	original := strings.Repeat("line\n", 5) + "two\n" + strings.Repeat("line\n", 10) + "eight\n" + strings.Repeat("line\n", 5)
	edited := strings.Replace(strings.Replace(original, "two\n", "TWO\n", 1), "eight\n", "EIGHT\n", 1)
	id := putTestChange(t, ctx, store, workspace, "edit", mekugi.RenderReviewFile("a.txt", "a.txt", original, edited))
	// One hunk was reverted by hand; the history cannot account for it.
	writeTestFile(t, filepath.Join(workspace, "a.txt"), strings.Replace(edited, "TWO\n", "two\n", 1))
	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", id)
	// Reapplying would also redo the hand-reverted hunk, so no inverse is exact.
	want := "undo: revert this command's change (newest in mchanges --list)\n" +
		"?? a.txt stat unavailable: file changed outside mchanges history · 1 hunk already reverted\n"
	if status != 0 || output != want || readTestFile(t, filepath.Join(workspace, "a.txt")) != original {
		t.Fatalf("hand-reverted = %d %q, want %q", status, output, want)
	}

	// An unreadable unrelated record degrades the stat without blocking.
	other := putTestChange(t, ctx, store, workspace, "other", mekugi.RenderReviewFile("", "b.txt", "", "b\n"))
	index, err := store.scoped(ctx).readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range index.Changes[other].Calls {
		if err := os.Remove(filepath.Join(store.directory, replayRecordName(workspace, call.ID, false))); err != nil {
			t.Fatal(err)
		}
	}
	output, status = mutateTestChanges(t, ctx, store, workspace, "apply", id)
	want = "undo: mchanges revert " + id + "\n?? a.txt stat unavailable: mchanges history could not be read\n"
	if status != 0 || output != want || readTestFile(t, filepath.Join(workspace, "a.txt")) != edited {
		t.Fatalf("history unavailable = %d %q, want %q", status, output, want)
	}
}

func TestMChangesRevertKeepsMoveSourceWhenDestinationFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	id := putTestChange(t, ctx, store, workspace, "move", mekugi.RenderReviewFile("locked/old.txt", "moved.txt", "", ""))
	writeTestFile(t, filepath.Join(workspace, "moved.txt"), "kept\n")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", id)
	if status != 1 || !strings.Contains(output, "?? locked/old.txt stat unavailable · write failed: ") ||
		!strings.Contains(output, "moved.txt unchanged · kept: move destination was not written\n") {
		t.Fatalf("failed move = %d %q", status, output)
	}
	if got := readTestFile(t, filepath.Join(workspace, "moved.txt")); got != "kept\n" {
		t.Fatalf("moved.txt = %q", got)
	}
}

func TestMChangesFrontendRevertsWorkspace(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "revert-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(ctx, workspace, "revert-thread", "frontend-edit")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: "frontend-edit", ReviewFiles: []mekugi.ReviewFile{
		mekugi.RenderReviewFile("a.txt", "a.txt", "one\ntwo\n", "one\nTWO\n"),
	}}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"frontend-edit": history}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "a.txt"), "one\nTWO\n")
	invocation := newShellWorkerTestInvocation(workspace,
		"XDG_STATE_HOME="+t.TempDir(), "MEKUGI_RUNTIME_DIR="+t.TempDir(), "CODEX_THREAD_ID=revert-thread",
		routerTestWorkerUnscopedEnvironment+"=0")
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "mchanges revert "+id, nil, invocation)
	if status != 0 || stderr != "" || stdout != "undo: mchanges apply "+id+"\n   a.txt clean\n" {
		t.Fatalf("frontend revert = %q, %q, %d", stdout, stderr, status)
	}
	if got := readTestFile(t, filepath.Join(workspace, "a.txt")); got != "one\ntwo\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestMChangesMutationTaintsLivePreview(t *testing.T) {
	for command, neutral := range map[string]bool{"mchanges amber1 --summary": true, "mchanges revert amber1": false, "mchanges apply amber1": false} {
		program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := liveDiffShellPreviewNeutral(program.Stmts[0]); got != neutral {
			t.Errorf("%q neutral = %v, want %v", command, got, neutral)
		}
	}
}

func TestMChangesRevertSwapsDirectoryAndFile(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	// rm -r x && echo new > x
	toFile := putTestChange(t, ctx, store, workspace, "to-file",
		mekugi.RenderReviewFile("x/a", "", "old\n", ""),
		mekugi.RenderReviewFile("", "x", "", "new\n"))
	writeTestFile(t, filepath.Join(workspace, "x"), "new\n")
	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", toFile)
	if status != 0 || readTestFile(t, filepath.Join(workspace, "x", "a")) != "old\n" {
		t.Fatalf("revert file swap = %d %q", status, output)
	}
	putTestChange(t, ctx, store, workspace, "revert-to-file",
		mekugi.RenderReviewFile("x", "", "new\n", ""),
		mekugi.RenderReviewFile("", "x/a", "", "old\n"))

	// Reapplying replaces the now-empty directory with the file.
	output, status = mutateTestChanges(t, ctx, store, workspace, "apply", toFile)
	if status != 0 || readTestFile(t, filepath.Join(workspace, "x")) != "new\n" {
		t.Fatalf("apply file swap = %d %q", status, output)
	}

	// A directory that still holds other files is never removed.
	writeTestFile(t, filepath.Join(workspace, "y", "a"), "old\n")
	writeTestFile(t, filepath.Join(workspace, "y", "user"), "keep\n")
	toDir := putTestChange(t, ctx, store, workspace, "y-to-file",
		mekugi.RenderReviewFile("y/a", "", "old\n", ""),
		mekugi.RenderReviewFile("", "y", "", "new\n"))
	output, status = mutateTestChanges(t, ctx, store, workspace, "apply", toDir)
	if status != 1 || !strings.Contains(output, "?? y stat unavailable · write failed: ") ||
		readTestFile(t, filepath.Join(workspace, "y", "user")) != "keep\n" {
		t.Fatalf("apply over nonempty directory = %d %q", status, output)
	}
}

func TestMChangesSkipsRecordedSymlinks(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	link := mekugi.RenderReviewFile("", "link", "", "-> target\n")
	link.Link = true
	flagged := putTestChange(t, ctx, store, workspace, "flagged", link)
	// Records made before ReviewFile.Link carry only the exec review text.
	legacyID, err := store.reserveChange(ctx, workspace, "stock-thread", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	legacy := mekugiHistory{ChangeID: legacyID, CorrelationID: "legacy", ExecObservation: &execObservation{},
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("", "old-link", "", "-> target\n")}}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"legacy": legacy}); err != nil {
		t.Fatal(err)
	}
	for id, path := range map[string]string{flagged: "link", legacyID: "old-link"} {
		output, status := mutateTestChanges(t, ctx, store, workspace, "apply", id)
		if status != 1 || !strings.Contains(output, "skipped: symlink changes are not replayed") {
			t.Fatalf("apply %s = %d %q", path, status, output)
		}
		if _, err := os.Lstat(filepath.Join(workspace, path)); !os.IsNotExist(err) {
			t.Fatalf("%s was written: %v", path, err)
		}
	}
}

func TestMChangesHistoryFollowsMovesToDroppedDiffs(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "stock-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	original, edited, later := "one\ntwo\n", "one\nTWO\n", "one\nTWO\nthree\n"
	// The edit under the old name is only relevant through the later move.
	putTestChange(t, ctx, store, workspace, "edit", mekugi.RenderReviewFile("old.txt", "old.txt", original, edited))
	putTestChange(t, ctx, store, workspace, "move", mekugi.RenderReviewFile("old.txt", "new.txt", edited, edited))
	last := putTestChange(t, ctx, store, workspace, "append", mekugi.RenderReviewFile("new.txt", "new.txt", edited, later))
	writeTestFile(t, filepath.Join(workspace, "new.txt"), later)
	output, status := mutateTestChanges(t, ctx, store, workspace, "revert", last)
	want := "undo: mchanges apply " + last + "\n R old.txt -> new.txt +1 -1\n"
	if status != 0 || output != want {
		t.Fatalf("revert = %d %q, want %q", status, output, want)
	}
}
