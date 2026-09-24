package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

type mchangesSliceFixture struct {
	registry   *toolRegistry
	store      *mekugiReplayStore
	ctx        context.Context
	workspace  string
	thread     string
	invocation shellWorkerTestInvocation
}

func newMChangesSliceFixture(t *testing.T, thread string) *mchangesSliceFixture {
	t.Helper()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), thread, "mchanges-slices")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	workspace := t.TempDir()
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
	id, err := f.store.reserveChange(f.ctx, f.workspace, thread, correlation)
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
	stream, _, err := parseChangeID(id)
	if err != nil {
		t.Fatal(err)
	}
	store := f.store.scoped(f.ctx)
	if err := store.locked(f.ctx, func() error {
		index, err := store.readChangeIndex(f.workspace)
		if err != nil {
			return err
		}
		if _, exists := index.Changes[id]; !exists {
			return fmt.Errorf("test change %s is not allocated", id)
		}
		position := -1
		for candidate := range index.Streams {
			if changeStreamName(candidate) == stream {
				position = candidate
				break
			}
		}
		if position < 0 {
			return fmt.Errorf("test stream %s is unavailable", stream)
		}
		delete(index.Changes, id)
		index.Streams[position].Retired++
		return store.writeChangeIndex(index)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMChangesUnconfirmedHistorySeparatesObservationAndResult(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "unconfirmed-status-detail")

	patchID := f.reserve(t, f.thread, "patch-observed")
	f.publish(t, patchID, "patch-observed", "patch-call", mekugiHistory{
		ToolName:    applyPatchToolName,
		Script:      "update file.txt",
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")},
	})
	patchHistory, patchErr, patchStatus := f.run(t, "mchanges --history "+patchID)
	if patchStatus != 0 || patchErr != "" || !strings.HasPrefix(patchHistory, patchID+" changes observed\n") ||
		!strings.Contains(patchHistory, "application confirmation: unavailable; observed changes do not establish tool success") {
		t.Fatalf("unconfirmed patch history = %q, %q, %d", patchHistory, patchErr, patchStatus)
	}

	execID := f.reserve(t, f.thread, "exec-observed")
	f.publish(t, execID, "exec-observed", "exec-call", mekugiHistory{
		ToolName: nativeExecCommandToolName,
		Script:   "text(await tools.exec_command({cmd: 'touch file.txt'}));\n",
		ExecOutcome: &execOutcome{
			Status: execStatusUnconfirmed, Class: "Code Mode", Coverage: execCoverageExact,
		},
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")},
	})
	execHistory, execErr, execStatus := f.run(t, "mchanges --history "+execID)
	if execStatus != 0 || execErr != "" || !strings.HasPrefix(execHistory, execID+" changes observed") ||
		!strings.Contains(execHistory, "tool result: nested tool result unavailable") {
		t.Fatalf("unconfirmed exec history = %q, %q, %d", execHistory, execErr, execStatus)
	}

	list, listErr, listStatus := f.run(t, "mchanges --list")
	if listStatus != 0 || listErr != "" || !strings.Contains(list, patchID+" observed ") ||
		!strings.Contains(list, execID+" observed ") || strings.Contains(list, "confirmation:") || strings.Contains(list, "tool result:") {
		t.Fatalf("mchanges list leaked history-only details or lost observed statuses: %q, %q, %d", list, listErr, listStatus)
	}
}

func TestMChangesSlicesUsageArgumentsAndWorkspaceErrors(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-arguments")
	manifest, err := readToolWorkerManifest(filepath.Join(f.registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var description string
	for _, contribution := range manifest.Tools {
		if contribution.Name != "mchanges" {
			continue
		}
		var definition struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(contribution.Specification, &definition); err != nil {
			t.Fatal(err)
		}
		description = definition.Description
		break
	}
	if !strings.Contains(description, "Usage: `"+changesReadUsage+"`") {
		t.Fatalf("agent-facing mchanges usage does not match parser usage: %q", description)
	}

	var ids []string
	for i := range 2 {
		correlation := fmt.Sprintf("argument-edit-%d", i+1)
		id := f.reserve(t, f.thread, correlation)
		ids = append(ids, id)
		before, after := fmt.Sprintf("before-%d\n", i+1), fmt.Sprintf("after-%d\n", i+1)
		f.publish(t, id, correlation, correlation+"-call", mekugiHistory{
			Applied:     true,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", before, after)},
		})
	}
	if ids[0] != "amber1" || ids[1] != "amber2" {
		t.Fatalf("fixture IDs = %v", ids)
	}
	stdout, stderr, status := f.run(t, "mchanges amber1..2 --summary")
	if status != 0 || stderr != "" || stdout != "2\t2\tfile.txt\n" {
		t.Fatalf("numeric range end: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = f.run(t, "mchanges "+ids[0]+" --summary")
	if status != 0 || stderr != "" || stdout != "1\t1\tfile.txt\n" {
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
	if status == 0 || stdout != "" || !strings.Contains(stderr, "never allocated") ||
		!strings.Contains(stderr, "amber4") {
		t.Fatalf("never-allocated diagnostic: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges "+retired+" --workspace ..", nil, childInvocation)
	if status == 0 || stdout != "" || !strings.Contains(stderr, "retired") {
		t.Fatalf("retired diagnostic: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesSlicesSummaryKeepsMixedStates(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "slice-mixed-summary")
	completed := f.reserve(t, f.thread, "summary-completed")
	f.publish(t, completed, "summary-completed", "summary-completed-call", mekugiHistory{
		Applied:     true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("kept.txt", "kept.txt", "old\n", "new\n")},
	})
	pending := f.reserve(t, f.thread, "summary-pending")
	retired := f.reserve(t, f.thread, "summary-retired")
	f.retire(t, retired)
	unknown := "amber4"
	stdout, stderr, status := f.run(t,
		"mchanges --summary "+strings.Join([]string{completed, pending, retired, unknown}, " "))
	if status != 0 || stderr != "" || !strings.Contains(stdout, "1\t1\tkept.txt\n") ||
		!strings.Contains(stdout, pending+" pending") || !strings.Contains(stdout, retired+" retired") ||
		!strings.Contains(stdout, unknown+" unknown") {
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
		Applied: true,
		Script:  input.String(),
	})
	pendingCorrelation := "pending-finalizes"
	pending := f.reserve(t, f.thread, pendingCorrelation)
	options := changeReadOptions{workspace: f.workspace, ids: []string{completed, pending}, view: "history"}
	expected, err := f.store.readChanges(f.ctx, options)
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
		Applied: true,
		Attempt: 2,
		Script:  "appended-after-page\n",
	})
	f.publish(t, pending, pendingCorrelation, "pending-finalized-call", mekugiHistory{
		Applied: true,
		Script:  "finalized-after-page\n",
	})
	updated, err := f.store.readChanges(f.ctx, options)
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
			Applied:     true,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("own.txt", "own.txt", fmt.Sprintf("old-%d\n", row), fmt.Sprintf("new-%d\n", row))},
		})
	}
	pending := f.reserve(t, f.thread, "own-pending")
	retired := f.reserve(t, f.thread, "own-retired")
	f.retire(t, retired)
	foreign := f.reserve(t, "slice-outsider", "outsider-edit")
	f.publish(t, foreign, "outsider-edit", "outsider-call", mekugiHistory{
		Applied:     true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("outsider.txt", "outsider.txt", "old\n", "new\n")},
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
	if status != 0 || stderr != "" || !strings.Contains(stdout, "3\t3\town.txt\n") ||
		!strings.Contains(stdout, pending+" pending") || !strings.Contains(stdout, retired+" retired") ||
		strings.Contains(stdout, "outsider.txt") {
		t.Fatalf("mine summary: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = f.run(t, "mchanges --list")
	if status != 0 || stderr != "" || strings.TrimSpace(stdout) != "amber1..amber3 applied +3 -3\namber4 pending\namber5 retired" ||
		strings.Contains(stdout, "apple1") {
		t.Fatalf("compressed own-thread list: %q, %q, %d", stdout, stderr, status)
	}

	child, releaseChild, err := f.store.beginSession(f.ctx, "slice-fork", "fork-routing")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseChild()
	bindTestHandleScope(t, f.store, child, "", f.thread)
	childInvocation := f.invocationFor(f.workspace, "slice-fork")
	stdout, stderr, status = runShellWorkerTest(t, f.registry, "bash", nil,
		"mchanges --mine --list", nil, childInvocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "amber1..amber3 applied +3 -3") ||
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
	if status != 0 || stderr != "" || !strings.Contains(stdout, "amber1..amber3 applied +3 -3") ||
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
	first := mekugi.RenderReviewFile(path, path, "old value\n", "intermediate value\n")
	second := mekugi.RenderReviewFile(path, path, "intermediate value\n", "final value\n")
	f.publish(t, id, correlation, "net-first-call", mekugiHistory{Applied: true, ReviewFiles: []mekugi.ReviewFile{first}})
	f.publish(t, id, correlation, "net-second-call", mekugiHistory{Applied: true, Attempt: 2, ReviewFiles: []mekugi.ReviewFile{second}})
	want := "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n@@ -1,1 +1,1 @@\n-old value\n+final value\n"
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
		Applied:     true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderIncompleteReviewFile("unknown.txt", "unknown.txt", "capture unavailable")},
	})
	stdout, stderr, status = f.run(t, "mchanges --net "+incomplete)
	if status == 0 || stdout != "" || !(strings.Contains(stderr, "incomplete") || strings.Contains(stderr, "pending")) {
		t.Fatalf("incomplete net evidence was not rejected: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesFrozenOverlapLabelSurvivesUnselectedRetirement(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "frozen-overlap")
	other := f.reserve(t, f.thread, "overlapping")
	f.publish(t, other, "overlapping", "overlap-call:attempt", mekugiHistory{Applied: true})
	selected := f.reserve(t, f.thread, "selected")
	f.publish(t, selected, "selected", "selected-call", mekugiHistory{
		Script: "captured input\n", ExecOutcome: &execOutcome{Status: execStatusCompleted, Coverage: execCoverageExact, Overlaps: []string{"overlap-call"}},
	})
	text, snapshot, err := f.store.readChangeView(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{selected}, view: "history"})
	if err != nil || !strings.Contains(text, "observed alongside "+other+"\n") {
		t.Fatalf("overlap snapshot: %q, %v", text, err)
	}
	offset := strings.IndexByte(text, '\n') + 1
	reference, err := f.store.putChangeRead(f.ctx, snapshot, text, offset)
	if err != nil {
		t.Fatal(err)
	}
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
	record, err := resumed.readShellOutput(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	output, err := resumed.readSourceStreams(ctx, record)
	if err != nil || output.Stdout != text[offset:] || output.StdoutKind != "rows" {
		t.Fatalf("frozen overlap recovery: %+v, %v", output, err)
	}
}

func TestMChangesNetWithoutOwnStreamExcludesSiblingChanges(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "fresh-own-with-sibling-edits")
	correlation := "sibling-only-change"
	sibling := f.reserve(t, "sibling-thread", correlation)
	f.publish(t, sibling, correlation, "sibling-only-call", mekugiHistory{
		Applied:     true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("sibling.txt", "sibling.txt", "old\n", "new\n")},
	})
	stdout, stderr, status := f.run(t, "mchanges --mine --net")
	if status != 0 || stderr != "" || stdout != "no net changes in selected captured history\n" ||
		strings.Contains(stdout, "sibling.txt") {
		t.Fatalf("empty own-thread net selection included sibling effects: %q, %q, %d", stdout, stderr, status)
	}
}

func TestMChangesNetRejectsUnconfirmedAndPartialCaptures(t *testing.T) {
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
		Script:          "text(await tools.exec_command({cmd: 'touch code-mode.txt'}));\n",
		ExecObservation: &execObservation{CodeMode: true},
		ExecOutcome:     &execOutcome{Status: execStatusUnconfirmed, Coverage: execCoverageExact},
		Applied:         false,
		ReviewFiles:     []mekugi.ReviewFile{mekugi.RenderReviewFile("code-mode.txt", "code-mode.txt", "before\n", "after\n")},
	})
	assertRejected(codeMode, "unconfirmed or partial captured effects")

	moveCorrelation := "unconfirmed-move-chain"
	move := f.reserve(t, f.thread, moveCorrelation)
	f.publish(t, move, moveCorrelation, "unconfirmed-move-call", mekugiHistory{
		ToolName:    nativeExecCommandToolName,
		Script:      "mv before.txt after.txt\n",
		ExecOutcome: &execOutcome{Status: execStatusUnconfirmed, Coverage: execCoverageExact},
		Applied:     false,
		ReviewFiles: []mekugi.ReviewFile{
			mekugi.RenderReviewFile("before.txt", "after.txt", "same content\n", "same content\n"),
		},
	})
	f.publish(t, move, moveCorrelation, "confirmed-edit-after-move", mekugiHistory{
		ToolName: applyPatchToolName,
		Script:   "update after.txt",
		Attempt:  2,
		Applied:  true,
		ReviewFiles: []mekugi.ReviewFile{
			mekugi.RenderReviewFile("after.txt", "after.txt", "same content\n", "confirmed edit\n"),
		},
	})
	assertRejected(move, "unconfirmed or partial captured effects")

	partialCorrelation := "partial-shell-capture"
	partial := f.reserve(t, f.thread, partialCorrelation)
	f.publish(t, partial, partialCorrelation, "partial-shell-call", mekugiHistory{
		ToolName:    nativeExecCommandToolName,
		Script:      "printf changed > partial.txt\n",
		ExecOutcome: &execOutcome{Status: execStatusCompleted, Coverage: execCoveragePartial},
		Applied:     true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("partial.txt", "partial.txt", "before\n", "after\n")},
	})
	assertRejected(partial, "unconfirmed or partial captured effects")
}

func TestMChangesNetRejectsBinaryOnlyAndMixedEvidence(t *testing.T) {
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
		Applied: true,
		ReviewFiles: []mekugi.ReviewFile{
			mekugi.RenderBinaryReviewFile("binary.dat", "binary.dat", 5, 7, "before-hash", "after-hash"),
		},
	})
	assertRejected(binary)

	mixedCorrelation := "mixed-binary-text"
	mixed := f.reserve(t, f.thread, mixedCorrelation)
	f.publish(t, mixed, mixedCorrelation, "mixed-binary-text-call", mekugiHistory{
		Applied: true,
		ReviewFiles: []mekugi.ReviewFile{
			mekugi.RenderBinaryReviewFile("mixed.dat", "mixed.dat", 2, 3, "old-hash", "new-hash"),
			mekugi.RenderReviewFile("text.txt", "text.txt", "before\n", "after\n"),
		},
	})
	assertRejected(mixed)
}
