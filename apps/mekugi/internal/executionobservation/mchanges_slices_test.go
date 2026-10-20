package executionobservation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/libraries/diffmodel"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type mchangesSliceFixture struct {
	registry   *frontends.Registry
	store      *mekugiReplayStore
	ctx        context.Context
	workspace  string
	thread     string
	invocation shellWorkerTestInvocation
}

func newMChangesSliceFixture(t *testing.T, thread string) *mchangesSliceFixture {
	t.Helper()
	registry := sharedProxyTestRegistry(t)
	thread = uniqueTestThread(thread)
	replayDirectory := registry.ReplayDirectory
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), thread, "mchanges-slices")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	workspace := t.TempDir()
	t.Cleanup(store.snapshots.Close)
	return &mchangesSliceFixture{
		registry:   registry,
		store:      store,
		ctx:        ctx,
		workspace:  workspace,
		thread:     thread,
		invocation: mchangesSliceInvocation(workspace, thread),
	}
}

func mchangesSliceInvocation(directory, thread string) shellWorkerTestInvocation {
	// BASH_ENV can replace fixture frontends before the test script starts.
	// Keep the test isolated from the interactive shell configuration.
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, "CODEX_THREAD_ID=") ||
			strings.HasPrefix(entry, routerTestWorkerEnvironment+"=") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"XDG_STATE_HOME="+filepath.Join(directory, ".state"),
		"MEKUGI_RUNTIME_DIR="+filepath.Join(directory, ".runtime"),
		"CODEX_THREAD_ID="+thread,
		routerTestWorkerUnscopedEnvironment+"=0",
	)
	return shellWorkerTestInvocation{directory: directory, environment: environment}
}

func (f *mchangesSliceFixture) run(t *testing.T, command string) (stdout, stderr string, status int) {
	t.Helper()
	return runShellWorkerTest(t, f.registry, "bash", nil, command, nil, f.invocation)
}

func (f *mchangesSliceFixture) invocationFor(directory, thread string) shellWorkerTestInvocation {
	return mchangesSliceInvocation(directory, thread)
}

func (f *mchangesSliceFixture) reserve(t *testing.T, thread, correlation string) string {
	t.Helper()
	id, err := f.store.changes.Reserve(f.ctx, f.store.scoped(f.ctx).changeScope(), f.workspace, thread, correlation)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *mchangesSliceFixture) publish(t *testing.T, id, correlation, callID string, history mekugiHistory) {
	t.Helper()
	history.ChangeID, history.CorrelationID = id, correlation
	if history.ToolName == "" {
		history.ToolName = applyPatchToolName
	}
	if history.Script == "" {
		history.Script = callID
	}
	if err := f.store.put(f.ctx, f.workspace, map[string]mekugiHistory{callID: history}); err != nil {
		t.Fatal(err)
	}
}

func (f *mchangesSliceFixture) retire(t *testing.T, id string) {
	t.Helper()
	stream, _, err := changes.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	store := f.store.scoped(f.ctx)
	if err := store.locked(f.ctx, func(store *mekugiReplayStore) error {
		index, err := store.changes.Inspect(store.changeScope(), f.workspace)
		if err != nil {
			return err
		}
		if _, exists := index.Changes[id]; !exists {
			return fmt.Errorf("test change %s is not allocated", id)
		}
		position := -1
		for candidate := range index.Streams {
			if index.Streams[candidate].Name == stream {
				position = candidate
				break
			}
		}
		if position < 0 {
			return fmt.Errorf("test stream %s is unavailable", stream)
		}
		delete(index.Changes, id)
		index.Streams[position].Retired++
		return writeChangeIndexFixture(store, index)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMChangesHistoryReportsSavedEffectsWithoutCommandResult(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "unconfirmed-status-detail")

	patchID := f.reserve(t, f.thread, "patch-observed")
	f.publish(t, patchID, "patch-observed", "patch-call", mekugiHistory{
		ToolName:    applyPatchToolName,
		Script:      "update file.txt",
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")},
	})
	patchHistory, patchErr, patchStatus := f.run(t, "mchanges --history "+patchID)
	if patchStatus != 0 || patchErr != "" || !strings.HasPrefix(patchHistory, patchID+" applied\n") ||
		strings.Contains(patchHistory, "confirmation") {
		t.Fatalf("saved patch history = %q, %q, %d", patchHistory, patchErr, patchStatus)
	}

	execID := f.reserve(t, f.thread, "exec-observed")
	f.publish(t, execID, "exec-observed", "exec-call", mekugiHistory{
		ToolName: nativeExecCommandToolName,
		Script:   "text(await tools.exec_command({cmd: 'touch file.txt'}));\n",
		ExecOutcome: &changecapture.Outcome{
			Status: "", Class: "exec", Coverage: changecapture.CoverageExact,
		},
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")},
	})
	execHistory, execErr, execStatus := f.run(t, "mchanges --history "+execID)
	if execStatus != 0 || execErr != "" || !strings.HasPrefix(execHistory, execID+" applied") ||
		strings.Contains(execHistory, "nested tool result unavailable") {
		t.Fatalf("saved exec history = %q, %q, %d", execHistory, execErr, execStatus)
	}

	list, listErr, listStatus := f.run(t, "mchanges --list")
	if listStatus != 0 || listErr != "" || !strings.Contains(list, patchID+" +1 -1\n") ||
		!strings.Contains(list, execID+" +1 -1\n") || strings.Contains(list, "observed") || strings.Contains(list, "coverage") {
		t.Fatalf("mchanges list leaked history-only details or lost change IDs: %q, %q, %d", list, listErr, listStatus)
	}
}

func TestMChangesSlicesUsageArgumentsAndWorkspaceErrors(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-arguments")
	description := f.registry.FrontendGuidance.Descriptions
	_, usageErr := testChanges().Parse([]string{"--invalid"}, f.workspace)
	agentUsage := strings.Replace(strings.TrimPrefix(usageErr.Error(), "unknown option --invalid; "), "--summary|--history|--net", "--summary|--net", 1)
	if !strings.Contains(description, "Usage: `"+agentUsage+"`") {
		t.Fatalf("agent-facing mchanges usage does not match parser usage: %q", description)
	}

	var ids []string
	for i := range 2 {
		correlation := fmt.Sprintf("argument-edit-%d", i+1)
		id := f.reserve(t, f.thread, correlation)
		ids = append(ids, id)
		before, after := fmt.Sprintf("before-%d\n", i+1), fmt.Sprintf("after-%d\n", i+1)
		if i == 1 {
			before = "after-1\n"
		}
		f.publish(t, id, correlation, correlation+"-call", mekugiHistory{
			ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", before, after)},
		})
	}
	if ids[0] != "amber1" || ids[1] != "amber2" {
		t.Fatalf("fixture IDs = %v", ids)
	}
	stdout, stderr, status := f.run(t, "mchanges amber1..2 --summary")
	if status != 0 || stderr != "" || stdout != "M\t1\t1\tfile.txt\n" {
		t.Fatalf("numeric range end: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = f.run(t, "mchanges "+ids[0]+" --summary")
	if status != 0 || stderr != "" || stdout != "M\t1\t1\tfile.txt\n" {
		t.Fatalf("explicit ID read: %q, %q, %d", stdout, stderr, status)
	}
	for _, test := range []struct {
		command string
		message string
	}{
		{"mchanges -n 5", "option"},
		{"mchanges " + ids[0] + " file.txt", "-- PATH"},
	} {
		stdout, stderr, status = f.run(t, test.command)
		if status == 0 || stdout != "" || !strings.Contains(stderr, test.message) {
			t.Errorf("%s: %q, %q, %d; want error mentioning %q", test.command, stdout, stderr, status, test.message)
		}
	}

	retired := f.reserve(t, f.thread, "retired-change")
	f.retire(t, retired)
	latest := f.reserve(t, f.thread, "latest-change")
	if latest != "amber4" {
		t.Fatalf("latest allocated ID = %s; want amber4", latest)
	}
	child := filepath.Join(f.workspace, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	childInvocation := f.invocationFor(child, f.thread)
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges "+ids[0], nil, childInvocation)
	if status == 0 || stdout != "" || !strings.Contains(stderr, "--workspace") {
		t.Fatalf("wrong-workspace hint: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges amber5 --workspace ..", nil, childInvocation)
	if status == 0 || stdout != "available IDs: amber1..amber2\navailable IDs: amber4\n" || !strings.Contains(stderr, "never allocated") ||
		!strings.Contains(stderr, "amber4") {
		t.Fatalf("never-allocated diagnostic: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges "+retired+" --workspace ..", nil, childInvocation)
	if status == 0 || stdout != "available IDs: amber1..amber2\navailable IDs: amber4\n" || !strings.Contains(stderr, "retired") {
		t.Fatalf("retired diagnostic: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesSlicesSummaryKeepsMixedStates(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-mixed-summary")
	completed := f.reserve(t, f.thread, "summary-completed")
	f.publish(t, completed, "summary-completed", "summary-completed-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("kept.txt", "kept.txt", "old\n", "new\n")},
	})
	pending := f.reserve(t, f.thread, "summary-pending")
	retired := f.reserve(t, f.thread, "summary-retired")
	f.retire(t, retired)
	unknown := "amber4"
	stdout, stderr, status := f.run(t,
		"mchanges --summary "+strings.Join([]string{completed, pending, retired, unknown}, " "))
	if status != 1 || !strings.Contains(stderr, retired) || !strings.Contains(stderr, unknown) || !strings.Contains(stdout, "M\t1\t1\tkept.txt\n") ||
		!strings.Contains(stdout, pending+" pending") || strings.Contains(stdout, retired) || strings.Contains(stdout, unknown) {
		t.Fatalf("mixed summary: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesSlicesPageKeepsOriginalPendingAndAttemptSnapshot(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-page-snapshot")
	correlation := "large-completed"
	completed := f.reserve(t, f.thread, correlation)
	var input strings.Builder
	for row := range 180 {
		fmt.Fprintf(&input, "original-row-%03d\n", row)
	}
	f.publish(t, completed, correlation, "large-completed-call", mekugiHistory{
		Script: input.String(),
	})
	pendingCorrelation := "pending-finalizes"
	pending := f.reserve(t, f.thread, pendingCorrelation)
	options := changes.Query{Workspace: f.workspace, IDs: []string{completed, pending}, View: "history"}
	expected, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expected, pending+" pending") || strings.Contains(expected, "finalized-after-page") {
		t.Fatalf("bad pre-page fixture: %q", expected[len(expected)-min(len(expected), 200):])
	}
	stdout, stderr, status := f.run(t, "mchanges --mine --history --max-tokens 400")
	if status == 0 || stdout == "" || !strings.HasPrefix(stderr, "read: incomplete; next_call: mread ") ||
		!strings.HasSuffix(stdout, "\n") {
		t.Fatalf("first page: %q, %q, %d", stdout, stderr, status)
	}
	stitched := stdout
	cursor := strings.Fields(strings.TrimPrefix(stderr, "read: incomplete; next_call: mread "))[0]

	// Finalize a previously pending selected change and append another attempt
	// to the selected completed change while the original read is being paged.
	f.publish(t, completed, correlation, "completed-appended-attempt", mekugiHistory{
		Attempt: 2,
		Script:  "appended-after-page\n",
	})
	f.publish(t, pending, pendingCorrelation, "pending-finalized-call", mekugiHistory{
		Script: "finalized-after-page\n",
	})
	updated, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), options)
	if err != nil {
		t.Fatal(err)
	}
	if updated == expected || !strings.Contains(updated, "finalized-after-page") ||
		!strings.Contains(updated, "appended-after-page") {
		t.Fatal("fixture mutations did not change the selected review state")
	}
	pages := 1
	for page := range 100 {
		stdout, stderr, status = f.run(t, "mread "+cursor+" --stdout --max-tokens 400")
		if stdout != "" && !strings.HasSuffix(stdout, "\n") {
			t.Fatalf("page %d ends within a row: %q", page+2, stdout)
		}
		stitched += stdout
		pages = page + 2
		if status == 0 {
			if stderr != "" {
				t.Fatalf("final page diagnostic: %q", stderr)
			}
			break
		}
		const marker = "read: incomplete; next_call: mread "
		if !strings.HasPrefix(stderr, marker) || stdout == "" || page == 99 {
			t.Fatalf("continuation page %d: %q, %q, %d", page+2, stdout, stderr, status)
		}
		cursor = strings.Fields(strings.TrimPrefix(stderr, marker))[0]
	}
	if pages < 3 {
		t.Fatalf("snapshot read used %d pages; want continuation beyond the mutation page", pages)
	}
	if stitched != expected || !strings.Contains(stitched, pending+" pending") ||
		strings.Contains(stitched, "finalized-after-page") || strings.Contains(stitched, "appended-after-page") {
		t.Fatalf("continuation did not preserve the original snapshot: got %q, want %q", stitched, expected)
	}
}

func TestMChangesSlicesMineListForkAndResume(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-root")
	for row := range 3 {
		correlation := fmt.Sprintf("mine-%d", row+1)
		id := f.reserve(t, f.thread, correlation)
		f.publish(t, id, correlation, correlation+"-call", mekugiHistory{
			ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("own.txt", "own.txt", fmt.Sprintf("state-%d\n", row), fmt.Sprintf("state-%d\n", row+1))},
		})
	}
	pending := f.reserve(t, f.thread, "own-pending")
	retired := f.reserve(t, f.thread, "own-retired")
	f.retire(t, retired)
	foreign := f.reserve(t, "slice-outsider", "outsider-edit")
	f.publish(t, foreign, "outsider-edit", "outsider-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("outsider.txt", "outsider.txt", "old\n", "new\n")},
	})
	if foreign != "apple1" || pending != "amber4" || retired != "amber5" {
		t.Fatalf("fixture IDs: own pending %s, own retired %s, foreign %s", pending, retired, foreign)
	}
	var bareOutput, mineOutput string
	for _, command := range []string{"mchanges", "mchanges --mine"} {
		stdout, stderr, status := f.run(t, command)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "own.txt") ||
			strings.Contains(stdout, "outsider.txt") {
			t.Errorf("%s did not read only the calling thread: %q, %q, %d", command, stdout, stderr, status)
		}
		if command == "mchanges" {
			bareOutput = stdout
		} else {
			mineOutput = stdout
		}
	}
	if bareOutput != mineOutput {
		t.Fatalf("bare mchanges differs from --mine: %q vs %q", bareOutput, mineOutput)
	}
	stdout, stderr, status := f.run(t, "mchanges --mine --summary")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "M\t1\t1\town.txt\n") ||
		!strings.Contains(stdout, pending+" pending") || !strings.Contains(stdout, retired+" retired") ||
		strings.Contains(stdout, "outsider.txt") {
		t.Fatalf("mine summary: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = f.run(t, "mchanges --list")
	if status != 0 || stderr != "" || strings.TrimSpace(stdout) != "amber1..amber3 +3 -3\namber4 pending\namber5 retired" ||
		strings.Contains(stdout, "apple1") {
		t.Fatalf("compressed own-thread list: %q, %q, %d", stdout, stderr, status)
	}

	fork := uniqueTestThread("slice-fork")
	child, releaseChild, err := f.store.beginSession(f.ctx, fork, "fork-routing")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseChild()
	bindTestHandleScope(t, f.store, child, "", f.thread)
	childInvocation := f.invocationFor(f.workspace, fork)
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges --mine --list", nil, childInvocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "amber1..amber3 +3 -3") ||
		!strings.Contains(stdout, "amber5 retired") ||
		strings.Contains(stdout, "apple1") {
		t.Fatalf("forked thread lost own review lineage: %q, %q, %d", stdout, stderr, status)
	}

	resumed, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	resumeCtx, releaseResume, err := resumed.beginSession(t.Context(), f.thread, "fresh-routing-process")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseResume()
	if resumeCtx == nil {
		t.Fatal("resumed thread context is nil")
	}
	resumeInvocation := f.invocationFor(f.workspace, f.thread)
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges --mine --list", nil, resumeInvocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "amber1..amber3 +3 -3") ||
		!strings.Contains(stdout, "amber5 retired") ||
		strings.Contains(stdout, "apple1") {
		t.Fatalf("resumed thread lost own review lineage: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesSlicesNetComposesRepeatedEditsAndRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-net")
	correlation := "net-repeated-edits"
	id := f.reserve(t, f.thread, correlation)
	path := filepath.Join(f.workspace, "target.txt")
	first := diffmodel.RenderReviewFile(path, path, "old value\n", "intermediate value\n")
	second := diffmodel.RenderReviewFile(path, path, "intermediate value\n", "final value\n")
	f.publish(t, id, correlation, "net-first-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{first}})
	f.publish(t, id, correlation, "net-second-call", mekugiHistory{Attempt: 2, ReviewFiles: []diffmodel.ReviewFile{second}})
	want := "--- \"target.txt\"\n+++ \"target.txt\"\n@@ -1,1 +1,1 @@\n-old value\n+final value\n"
	stdout, stderr, status := f.run(t, "mchanges --net "+id)
	if status != 0 || stderr != "" || stdout != want {
		t.Fatalf("net composition = %q, %q, %d; want %q", stdout, stderr, status, want)
	}
	stdout, stderr, status = f.run(t, "mchanges --mine --net")
	if status != 0 || stderr != "" || stdout != want {
		t.Fatalf("mine net composition = %q, %q, %d; want %q", stdout, stderr, status, want)
	}

	pending := f.reserve(t, f.thread, "net-pending")
	stdout, stderr, status = f.run(t, "mchanges --net "+pending)
	if status == 0 || stdout != "" || !(strings.Contains(stderr, "pending") || strings.Contains(stderr, "incomplete")) {
		t.Fatalf("pending net evidence was not rejected: %q, %q, %d", stdout, stderr, status)
	}
	incompleteCorrelation := "net-incomplete"
	incomplete := f.reserve(t, f.thread, incompleteCorrelation)
	f.publish(t, incomplete, incompleteCorrelation, "net-incomplete-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderIncompleteReviewFile("unknown.txt", "unknown.txt", "capture unavailable")},
	})
	stdout, stderr, status = f.run(t, "mchanges --net "+incomplete)
	if status == 0 || stdout != "" || !(strings.Contains(stderr, "incomplete") || strings.Contains(stderr, "pending")) {
		t.Fatalf("incomplete net evidence was not rejected: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesNetGitRoundTrip(t *testing.T) {
	t.Parallel()
	initial := strings.Repeat("original row\r\n", 149)
	appended := initial + strings.Repeat("appended row\r\n", 11) + "last row"
	for _, test := range []struct {
		name               string
		before, mid, after string
		create, remove     bool
	}{
		{"creation-append", "", initial, appended, true, false},
		{"creation-prepend", "", initial, "first row\r\n" + initial, true, false},
		{"deletion", appended, initial, "", false, true},
		{"modification", initial, "first row\r\n" + initial, "first row\r\n" + appended, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newMChangesSliceFixture(t, "net-git-"+test.name)
			gitTestRun(t, f.workspace, "init", "--quiet")
			path := filepath.Join(f.workspace, "file.txt")
			if !test.create {
				writeTestFile(t, path, test.before)
				gitTestRun(t, f.workspace, "-c", "core.autocrlf=false", "add", "--", "file.txt")
			}
			beforePath, afterPath := path, path
			if test.create {
				beforePath = ""
			}
			if test.remove {
				afterPath = ""
			}
			first := f.reserve(t, f.thread, "first")
			f.publish(t, first, "first", "first-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{
				diffmodel.RenderReviewFile(beforePath, path, test.before, test.mid),
			}})
			last := f.reserve(t, f.thread, "last")
			f.publish(t, last, "last", "last-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{
				diffmodel.RenderReviewFile(path, afterPath, test.mid, test.after),
			}})
			patch, stderr, status := f.run(t, "mchanges --net "+first+".."+last+" --max-tokens 15500")
			if status != 0 || stderr != "" {
				t.Fatalf("net read: %q, %q, %d", patch, stderr, status)
			}
			git := func(input string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir = f.workspace
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s\npatch:\n%s", args, err, out, patch)
				}
				return string(out)
			}
			git(patch, "apply", "--cached", "--unidiff-zero", "--whitespace=nowarn", "-p0")
			if test.remove {
				if out := git("", "ls-files"); out != "" {
					t.Fatalf("deleted file remains in index: %q", out)
				}
			} else if out := git("", "show", ":file.txt"); out != test.after {
				t.Fatalf("staged bytes = %q; want %q", out, test.after)
			}
			if (test.create || test.remove) && strings.Count(patch, "\n+++ ") != 1 {
				t.Fatalf("creation or deletion has multiple file sections:\n%s", patch)
			}
		})
	}
}

func TestMChangesFrozenOverlapLabelSurvivesUnselectedRetirement(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "frozen-overlap")
	other := f.reserve(t, f.thread, "overlapping")
	f.publish(t, other, "overlapping", "overlap-call:attempt", mekugiHistory{})
	selected := f.reserve(t, f.thread, "selected")
	f.publish(t, selected, "selected", "selected-call", mekugiHistory{
		Script: "captured input\n", ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusCompleted, Coverage: changecapture.CoverageExact, Overlaps: []string{"overlap-call"}},
	})
	text, diag, status := f.run(t, "mchanges "+selected+" --history --max-tokens 15500")
	if status != 0 || diag != "" || !strings.Contains(text, "observed alongside "+other+"\n") {
		t.Fatalf("overlap read: %q %q %d", text, diag, status)
	}
	shown, diag, status := f.run(t, "mchanges "+selected+" --history --max-tokens 12")
	const marker = "read: incomplete; next_call: mread "
	if status != 1 || shown == "" || !strings.HasPrefix(diag, marker) {
		t.Fatalf("bounded read: %q %q %d", shown, diag, status)
	}
	reference := strings.TrimSpace(strings.TrimPrefix(diag, marker))
	offset := len(shown)
	f.retire(t, other)
	resumed, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := resumed.beginSession(t.Context(), f.thread, "new-router")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	output, err := resumed.recoverOutput(ctx, reference)
	if err != nil || output.Stdout != text[offset:] {
		t.Fatalf("frozen overlap recovery: %+v, %v", output, err)
	}
}

func TestMChangesNetWithoutOwnStreamExcludesSiblingChanges(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "fresh-own-with-sibling-edits")
	correlation := "sibling-only-change"
	sibling := f.reserve(t, "sibling-thread", correlation)
	f.publish(t, sibling, correlation, "sibling-only-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("sibling.txt", "sibling.txt", "old\n", "new\n")},
	})
	stdout, stderr, status := f.run(t, "mchanges --mine --net")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "no captures selected") || !strings.Contains(stdout, "use explicit IDs") ||
		strings.Contains(stdout, "sibling.txt") {
		t.Fatalf("empty own-thread net selection included sibling effects: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesNetLabelsUnconfirmedAndRejectsPartialCaptures(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "net-capture-confidence")
	assertRejected := func(id, reason string) {
		t.Helper()
		stdout, stderr, status := f.run(t, "mchanges --net "+id)
		if status == 0 || stdout != "" || !strings.Contains(stderr, reason) ||
			!strings.Contains(stderr, "without --net to inspect") {
			t.Errorf("net accepted or obscured %s capture: %q, %q, %d", reason, stdout, stderr, status)
		}
	}

	codeModeCorrelation := "unconfirmed-codemode"
	codeMode := f.reserve(t, f.thread, codeModeCorrelation)
	f.publish(t, codeMode, codeModeCorrelation, "unconfirmed-codemode-call", mekugiHistory{
		ToolName:        nativeExecCommandToolName,
		Script:          "text(await tools.exec_command({cmd: 'touch exec.txt'}));\n",
		ExecObservation: &changecapture.Observation{CodeMode: true},
		ExecOutcome:     &changecapture.Outcome{Status: "", Coverage: changecapture.CoverageExact},
		ReviewFiles:     []diffmodel.ReviewFile{diffmodel.RenderReviewFile("exec.txt", "exec.txt", "before\n", "after\n")},
	})
	stdout, stderr, status := f.run(t, "mchanges --net "+codeMode)
	if status != 0 || stderr != "" || strings.Contains(stdout, "composing observed effects") || !strings.Contains(stdout, "-before\n+after\n") {
		t.Fatalf("unconfirmed capture lost its diff or outcome: %q, %q, %d", stdout, stderr, status)
	}

	moveCorrelation := "unconfirmed-move-chain"
	move := f.reserve(t, f.thread, moveCorrelation)
	f.publish(t, move, moveCorrelation, "unconfirmed-move-call", mekugiHistory{
		ToolName:    nativeExecCommandToolName,
		Script:      "mv before.txt after.txt\n",
		ExecOutcome: &changecapture.Outcome{Status: "", Coverage: changecapture.CoverageExact},
		ReviewFiles: []diffmodel.ReviewFile{
			diffmodel.RenderReviewFile("before.txt", "after.txt", "same content\n", "same content\n"),
		},
	})
	f.publish(t, move, moveCorrelation, "confirmed-edit-after-move", mekugiHistory{
		ToolName: applyPatchToolName,
		Script:   "update after.txt",
		Attempt:  2,
		ReviewFiles: []diffmodel.ReviewFile{
			diffmodel.RenderReviewFile("after.txt", "after.txt", "same content\n", "confirmed edit\n"),
		},
	})
	stdout, stderr, status = f.run(t, "mchanges --net "+move)
	if status != 0 || stderr != "" || strings.Contains(stdout, "composing observed effects") || !strings.Contains(stdout, "+confirmed edit\n") {
		t.Fatalf("unconfirmed move lost its diff or outcome: %q, %q, %d", stdout, stderr, status)
	}

	partialCorrelation := "partial-shell-capture"
	partial := f.reserve(t, f.thread, partialCorrelation)
	f.publish(t, partial, partialCorrelation, "partial-shell-call", mekugiHistory{
		ToolName:    nativeExecCommandToolName,
		Script:      "printf changed > partial.txt\n",
		ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusCompleted, Coverage: changecapture.CoveragePartial},
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("partial.txt", "partial.txt", "before\n", "after\n")},
	})
	assertRejected(partial, "partial captured effects")
}

func TestMChangesNetKeepsTextWhenBinaryEvidenceFails(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "net-binary-evidence")
	assertRejected := func(id string) {
		t.Helper()
		stdout, stderr, status := f.run(t, "mchanges --net "+id)
		if status == 0 || stdout != "" || !strings.Contains(stderr, "binary evidence that cannot be composed") ||
			!strings.Contains(stderr, "without --net to inspect") {
			t.Errorf("binary net evidence was not rejected with a review hint: %q, %q, %d", stdout, stderr, status)
		}
	}

	binaryCorrelation := "binary-only"
	binary := f.reserve(t, f.thread, binaryCorrelation)
	f.publish(t, binary, binaryCorrelation, "binary-only-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{
			diffmodel.RenderBinaryReviewFile("binary.dat", "binary.dat", 5, 7, "before-hash", "after-hash"),
		},
	})
	assertRejected(binary)

	mixedCorrelation := "mixed-binary-text"
	mixed := f.reserve(t, f.thread, mixedCorrelation)
	f.publish(t, mixed, mixedCorrelation, "mixed-binary-text-call", mekugiHistory{
		ReviewFiles: []diffmodel.ReviewFile{
			diffmodel.RenderBinaryReviewFile("mixed.dat", "mixed.dat", 2, 3, "old-hash", "new-hash"),
			diffmodel.RenderReviewFile("text.txt", "text.txt", "before\n", "after\n"),
		},
	})
	stdout, stderr, status := f.run(t, "mchanges --net "+mixed)
	if status != 1 || !strings.Contains(stdout, "text.txt") || !strings.Contains(stdout, "+after") || strings.Contains(stdout, "mixed.dat") || !strings.Contains(stderr, "binary evidence that cannot be composed") {
		t.Fatalf("mixed net evidence: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesMissingReadRecoversAvailableRange(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "missing-read-recovery")
	for number := 1; number <= 5; number++ {
		correlation := fmt.Sprintf("capture-%d", number)
		id := f.reserve(t, f.thread, correlation)
		f.publish(t, id, correlation, correlation+"-call", mekugiHistory{
			ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "old\n", "new\n")},
		})
	}
	// Explicit reads can cross threads, but recovery must not advertise unrelated streams.
	f.reserve(t, "other-thread", "unrelated")
	for _, view := range []string{"", "--summary", "--history", "--net", "--list"} {
		out, diagnostic, status := f.run(t, "mchanges amber6 "+view)
		if status != 1 || out != "available IDs: amber1..amber5\n" ||
			diagnostic != "mchanges: change amber6 is never allocated (latest is amber5)\n" {
			t.Fatalf("%s: stdout=%q stderr=%q status=%d", view, out, diagnostic, status)
		}
	}
	for _, operation := range []string{"apply", "revert"} {
		out, diagnostic, status := f.run(t, "mchanges "+operation+" amber1 amber6")
		if status != 1 || out != "" || !strings.Contains(diagnostic, "amber6") {
			t.Fatalf("%s: stdout=%q stderr=%q status=%d", operation, out, diagnostic, status)
		}
		if _, err := os.Stat(filepath.Join(f.workspace, "file.txt")); !os.IsNotExist(err) {
			t.Fatalf("%s mutated the workspace: %v", operation, err)
		}
	}
}

func TestMChangesMissingRangeDiagnostics(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "missing-range-diagnostics")
	for number := 1; number <= 5; number++ {
		correlation := fmt.Sprintf("capture-%d", number)
		id := f.reserve(t, f.thread, correlation)
		if number == 2 || number == 3 || number == 5 {
			f.retire(t, id)
		} else {
			name := fmt.Sprintf("file-%d.txt", number)
			f.publish(t, id, correlation, correlation+"-call", mekugiHistory{
				ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(name, name, "old\n", "new\n")},
			})
		}
	}
	for _, view := range []string{"", "--summary", "--history", "--net", "--list"} {
		for _, test := range []struct {
			selection, diagnostic string
			available             bool
		}{
			{"apple1..apple12", fmt.Sprintf("changes apple1..apple12 are never allocated (latest is none in this stream) in workspace %q; check --workspace", f.workspace), false},
			{"amber1..amber7", "changes amber2..amber3 are retired by session retention\nchange amber5 is retired by session retention\nchanges amber6..amber7 are never allocated (latest is amber5)", true},
			{"amber6 amber8", "change amber6 is never allocated (latest is amber5)\nchange amber8 is never allocated (latest is amber5)", false},
		} {
			out, diagnostic, status := f.run(t, "mchanges "+view+" "+test.selection)
			if status != 1 || diagnostic != "mchanges: "+test.diagnostic+"\n" {
				t.Fatalf("%s %s: stdout=%q stderr=%q status=%d", view, test.selection, out, diagnostic, status)
			}
			if test.available {
				first, last := "file-1.txt", "file-4.txt"
				if view == "--list" {
					first, last = "amber1", "amber4"
				}
				if !strings.Contains(out, first) || !strings.Contains(out, last) {
					t.Fatalf("%s lost available targets: %q", view, out)
				}
			}
		}
	}
}

func TestMChangesReadsKeepAvailableTargets(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "mixed-read-targets")
	first := f.reserve(t, f.thread, "first")
	f.publish(t, first, "first", "first-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("first.txt", "first.txt", "old\n", "first-new\n")}})
	second := f.reserve(t, f.thread, "second")
	f.publish(t, second, "second", "second-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("second.txt", "second.txt", "old\n", "second-new\n")}})
	for _, view := range []string{"", "--summary", "--history", "--net", "--list"} {
		for _, selection := range []string{first + " amber3 " + second, "amber1..amber3"} {
			out, diagnostic, status := f.run(t, "mchanges "+view+" "+selection)
			if status != 1 || !strings.Contains(diagnostic, "amber3") || !strings.Contains(diagnostic, "never allocated") {
				t.Fatalf("%s %s: %q %q %d", view, selection, out, diagnostic, status)
			}
			if view == "--list" {
				if !strings.Contains(out, first+".."+second) {
					t.Fatalf("list lost available range: %q", out)
				}
			} else if !strings.Contains(out, "first.txt") || !strings.Contains(out, "second.txt") {
				t.Fatalf("%s lost available results: %q", view, out)
			}
		}
	}
}

func TestMChangesOverlappingCapturesKeepIndependentEvidence(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "overlapping-captures")
	// Shape of the retained amber1..6 incident: an overlapping observation
	// starts at index+12 even though the preceding capture ends at index+14.
	// Reordering or accepting the contradictory baseline would invent a net.
	files := [][]diffmodel.ReviewFile{
		{diffmodel.RenderReviewFile("first.txt", "first.txt", "old\n", "first\n")},
		{diffmodel.RenderReviewFile("wrap_test.go", "wrap_test.go", "index+10\n", "index+14\n"),
			diffmodel.RenderReviewFile("README.md", "README.md", "old\n", "documented\n")},
		{diffmodel.RenderReviewFile("wrap_test.go", "wrap_test.go", "index+12\n", "index+14\n"),
			diffmodel.RenderReviewFile("evidence.go", "evidence.go", "old\n", "captured\n")},
		{diffmodel.RenderReviewFile("fourth.txt", "fourth.txt", "old\n", "fourth\n")},
		{diffmodel.RenderReviewFile("reset_test.go", "reset_test.go", "driver.message(now)\nnow := time.Now()\n", "driver.message()\nnow := time.Now()\n")},
		{diffmodel.RenderReviewFile("reset_test.go", "reset_test.go", "driver.message()\nnow := time.Now()\n", "driver.message()\n")},
	}
	var ids []string
	for i, reviews := range files {
		correlation := fmt.Sprintf("overlap-%d", i)
		id := f.reserve(t, f.thread, correlation)
		f.publish(t, id, correlation, correlation+"-call", mekugiHistory{ReviewFiles: reviews})
		ids = append(ids, id)
	}
	for _, selection := range []string{ids[0] + ".." + ids[5], strings.Join([]string{ids[5], ids[3], ids[2], ids[1], ids[4], ids[0]}, " ")} {
		out, diagnostic, status := f.run(t, "mchanges --net "+selection)
		if status != 1 || !strings.Contains(diagnostic, ids[2]+" cannot be composed") ||
			!strings.Contains(diagnostic, "captured source does not match") || !strings.Contains(diagnostic, "without --net") {
			t.Fatalf("overlap diagnostic: %q %q %d", out, diagnostic, status)
		}
		for _, expected := range []string{"+first", "+documented", "+fourth", "-index+10\n+index+14", "-driver.message(now)", "-now := time.Now()", "+driver.message()"} {
			if !strings.Contains(out, expected) {
				t.Errorf("lost independent evidence %q: %q", expected, out)
			}
		}
		if strings.Contains(out, "index+12") {
			t.Fatalf("inconsistent source leaked into net: %q", out)
		}
	}
	out, diagnostic, status := f.run(t, "mchanges "+ids[0]+".."+ids[5])
	if status != 0 || diagnostic != "" || !strings.Contains(out, "-index+12\n+index+14") {
		t.Fatalf("ordinary read lost overlapping capture: %q %q %d", out, diagnostic, status)
	}
}
