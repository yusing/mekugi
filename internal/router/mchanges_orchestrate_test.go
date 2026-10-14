package router

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

func TestMChangesCleanedChildRecovery(t *testing.T) {
	u := orchestrateCleanupUI(t)
	replay, workspace := u.proxy.replayStore, u.session.cwd
	t.Cleanup(replay.snapshots.close)
	child := u.orchestrateThreads["child"].batch
	childCtx, releaseChild, err := replay.beginSession(t.Context(), "child", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseChild()
	for _, owner := range []struct {
		thread, workspace string
		ctx               context.Context
	}{
		{"main", workspace, u.ctx}, {"child", child.Cwd, childCtx},
	} {
		f := &mchangesSliceFixture{store: replay, ctx: owner.ctx, workspace: owner.workspace, thread: owner.thread}
		id := f.reserve(t, owner.thread, owner.thread+"-edit")
		if id != "amber1" {
			t.Fatal("fixture needs colliding IDs", id)
		}
		f.publish(t, id, owner.thread+"-edit", owner.thread+"-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "before\n", owner.thread+" result\n")}})
	}
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	orchestrateTargetMCPClient(t, u, "cleanup")("main", false)
	if _, err := os.Stat(child.Cwd); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove checkout", err)
	}
	batches, err := (&orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}).Snapshot(workspace, "main")
	if err != nil || batches[0].State != "removed" {
		t.Fatal("cleanup identity not retained", err)
	}
	reopened, err := openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.snapshots.close)
	ctx, release, err := reopened.beginSession(t.Context(), "main", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	t.Chdir(workspace)
	manifest := toolWorkerManifest{ReplayDirectory: reopened.directory}
	result := executeMChanges(ctx, manifest, []string{"amber1", "--workspace", child.Cwd})
	if result.ExitCode != 0 || result.Stderr != "" || !strings.Contains(result.Stdout, "+child result") {
		t.Fatalf("cleaned child read: %+v", result)
	}
	for _, operation := range []string{"apply", "revert"} {
		result := executeMChanges(ctx, manifest, []string{operation, "amber1", "--workspace", child.Cwd})
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "resolve workspace") {
			t.Fatalf("cleaned child mutation admitted: %+v", result)
		}
	}
}

func TestMChangesOrchestratedChildRecovery(t *testing.T) {
	t.Parallel()
	registry, err := buildToolRegistryForTest(t, t.Context(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replay.snapshots.close)
	u, request := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	mainWorkspace := u.session.cwd
	childWorkspace := u.orchestrateThreads["child"].batch.Cwd
	childCtx, release, err := replay.beginSession(t.Context(), "child", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := &mchangesSliceFixture{store: replay, ctx: childCtx, workspace: childWorkspace, thread: "child"}
	first := child.reserve(t, "child", "first")
	second := child.reserve(t, "child", "second")
	file := filepath.Join(childWorkspace, "file.txt")
	before, after := "old\n", strings.Repeat("new child line\n", 100)
	child.publish(t, first, "first", "first-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(file, file, before, after)}})
	child.publish(t, second, "second", "second-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(file, file, after, after+"last\n")}})
	writeTestFile(t, file, after+"last\n")
	native := child.reserve(t, "native-child", "native")
	child.publish(t, native, "native", "native-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("native.txt", "native.txt", "old\n", "native edit\n")}})
	run := func(thread, cwd, command string) (string, string, int) {
		t.Helper()
		// Every invocation starts a fresh authenticated worker with no UI,
		// live journal map, or live child process available to resolve evidence.
		return runShellWorkerTest(t, registry, "bash", nil, command, nil, mchangesSliceInvocation(cwd, thread))
	}
	t.Run("durable locator excludes unrelated journals", func(t *testing.T) {
		journal, _, err := readThreadJournal(replay, mainWorkspace, "main")
		if err != nil {
			t.Fatal(err)
		}
		journal.Workspace, journal.Thread = t.TempDir(), "unrelated"
		journal.Orchestration = &journalRun{Directory: t.TempDir(), Workspace: journal.Workspace, Main: "main"}
		if err := writeThreadJournal(replay, journal); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(replay.directory, journalFilename(journal.Workspace, journal.Thread))
		t.Cleanup(func() { _ = os.Remove(path) })
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "+new child line") {
			t.Fatalf("unrelated journal affected located run: %q %q %d", stdout, stderr, status)
		}
	})
	for _, view := range []string{"", "--summary", "--history", "--list", "--net"} {
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first+".."+second+" "+view)
		if status != 0 || stderr != "" || !strings.Contains(stdout, map[bool]string{true: first, false: "file.txt"}[view == "--list"]) {
			t.Fatalf("child %s read: %q %q %d", view, stdout, stderr, status)
		}
	}
	for _, command := range []string{"mchanges", "mchanges --mine", "mchanges --list"} {
		stdout, stderr, status := run("main", mainWorkspace, command)
		if status != 0 || stderr != "" || strings.Contains(stdout, first) || strings.Contains(stdout, "file.txt") {
			t.Fatalf("caller scope %s: %q %q %d", command, stdout, stderr, status)
		}
	}
	stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first+" --max-tokens 32")
	var combined strings.Builder
	combined.WriteString(stdout)
	for status != 0 {
		const marker = "read: incomplete; next_call: mread "
		if !strings.HasPrefix(stderr, marker) {
			t.Fatalf("paging: %q %q %d", stdout, stderr, status)
		}
		cursor := strings.Fields(strings.TrimPrefix(stderr, marker))[0]
		stdout, stderr, status = run("main", mainWorkspace, "mread "+cursor+" --stdout --max-tokens 400")
		combined.WriteString(stdout)
	}
	whole, _, _ := run("main", mainWorkspace, "mchanges "+first)
	if combined.String() != whole {
		t.Fatal("fresh worker recovery changed child evidence")
	}
	for _, thread := range []string{"unrelated", "child"} {
		stdout, _, status := run(thread, mainWorkspace, "mchanges "+first)
		if status == 0 || strings.Contains(stdout, "new child line") {
			t.Fatalf("foreign workspace read by %s: %q %d", thread, stdout, status)
		}
	}
	// A real sibling gets an independent namespace, with a colliding first ID.
	store := u.proxy.orchestration.store
	sibling, err := store.Prepare(t.Context(), mainWorkspace, "main", "sibling")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.BeginLaunch(t.Context(), mainWorkspace, "main", "sibling", "work", jsontext.Value(`{}`), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordThread(t.Context(), mainWorkspace, "main", "sibling", "sibling"); err != nil {
		t.Fatal(err)
	}
	siblingCtx, releaseSibling, err := replay.beginSession(t.Context(), "sibling", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSibling()
	for _, err := range []error{
		u.proxy.journals.initialize(siblingCtx, replay, sibling.Cwd, "sibling", "/root", ""),
		u.proxy.journals.bindIdentity(siblingCtx, replay, sibling.Cwd, "sibling", "", "/root", true),
		u.proxy.journals.bindRun(siblingCtx, replay, sibling.Cwd, "sibling", journalRun{Directory: store.Directory, Workspace: mainWorkspace, Main: "main"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []struct {
		ctx               context.Context
		workspace, thread string
	}{{siblingCtx, sibling.Cwd, "sibling"}, {u.ctx, mainWorkspace, "main"}} {
		f := &mchangesSliceFixture{store: replay, ctx: owner.ctx, workspace: owner.workspace, thread: owner.thread}
		id := f.reserve(t, owner.thread, owner.thread+"-edit")
		if id != first {
			t.Fatal("fixture must collide", id, first)
		}
		path := filepath.Join(owner.workspace, owner.thread+".txt")
		f.publish(t, id, owner.thread+"-edit", owner.thread+"-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(path, path, "before\n", "after\n")}})
		writeTestFile(t, path, "after\n")
	}
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+first)
	if status == 0 || stdout != "" || !strings.Contains(stderr, "ambiguous") {
		t.Fatalf("ambiguous ID: %q %q %d", stdout, stderr, status)
	}
	// Older receipts predate frozen selections. Their original workspace and
	// digest must still recover through a fresh worker despite child collisions.
	historical, _, err := replay.readLocalChangeView(u.ctx, changeReadOptions{workspace: mainWorkspace, ids: []string{first}})
	if err != nil {
		t.Fatal(err)
	}
	handles, err := replay.allocateHandles(u.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	offset := strings.IndexByte(historical, '\n') + 1
	reference, err := replay.putReadRecord(u.ctx, shellOutputRecord{Version: 1, ID: handles[0], Changes: &changeReadSnapshot{
		Workspace: mainWorkspace, IDs: []string{first}, Offset: offset, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(historical))),
	}})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status = run("main", mainWorkspace, "mread "+reference)
	if status != 0 || stderr != "" || stdout != historical[offset:] {
		t.Errorf("historical local recovery: %q %q %d", stdout, stderr, status)
	}
	assertSiblingUnretained := func() {
		t.Helper()
		catalog, err := replay.readRetainedSession(storageSessionName("main"))
		if err != nil || catalog.Files[journalFilename(sibling.Cwd, "sibling")] {
			t.Fatalf("unselected sibling retained: %v", err)
		}
	}
	assertSiblingUnretained()
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+second)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "+last") {
		t.Fatalf("unique numeric ID: %q %q %d", stdout, stderr, status)
	}
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+first+" "+native)
	if status == 0 || !strings.Contains(stdout, "native edit") || !strings.Contains(stderr, "ambiguous") {
		t.Fatalf("available child beside ambiguity: %q %q %d", stdout, stderr, status)
	}
	selected := " --workspace " + shellsyntax.Quote(childWorkspace)
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+first+selected+" --summary -- file.txt")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "file.txt") {
		t.Fatalf("explicit child workspace: %q %q %d", stdout, stderr, status)
	}
	stdout, _, status = run("sibling", sibling.Cwd, "mchanges "+first+selected)
	if status == 0 || strings.Contains(stdout, "new child line") {
		t.Fatalf("sibling access: %q %d", stdout, status)
	}
	for _, operation := range []string{"revert", "apply"} {
		for _, workspace := range []string{"", selected} {
			_, stderr, status := run("main", mainWorkspace, "mchanges "+operation+" "+first+workspace)
			if status == 0 || !strings.Contains(stderr, "read-only") || readTestFile(t, file) != after+"last\n" || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "after\n" {
				t.Fatalf("unsafe %s: %q %d", operation, stderr, status)
			}
		}
	}
	assertSiblingUnretained()
	_, stderr, status = run("main", mainWorkspace, "mchanges revert "+first+" --workspace "+shellsyntax.Quote(mainWorkspace))
	if status != 0 || stderr != "" || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "before\n" {
		t.Fatalf("local mutation: %q %d", stderr, status)
	}
	if err := os.Remove(filepath.Join(replay.directory, replayRecordName(childWorkspace, "second-call", false))); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"", "--summary", "--history", "--list", "--net"} {
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first+".."+second+selected+" "+view)
		if status == 0 || stdout == "" || !strings.Contains(stderr, second) {
			t.Fatalf("independent child error %s: %q %q %d", view, stdout, stderr, status)
		}
	}
	retired := child.reserve(t, "child", "retired")
	child.retire(t, retired)
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+retired)
	if status == 0 || !strings.Contains(stderr, "retired") || strings.Contains(stderr, "ambiguous") {
		t.Fatalf("retired numeric ownership: %q %q %d", stdout, stderr, status)
	}

	// Only the partial composite needs evidence above the 16 MiB output limit.
	main := &mchangesSliceFixture{store: replay, ctx: u.ctx, workspace: mainWorkspace, thread: "main"}
	main.reserve(t, "main", "pending-second")
	main.reserve(t, "main", "pending-third")
	local := main.reserve(t, "main", "local-page")
	remote := child.reserve(t, "native-child", "large-child")
	line := "+" + strings.Repeat("evidence ", 20) + "\n"
	for _, capture := range []struct {
		f               *mchangesSliceFixture
		id, correlation string
		rows            int
	}{{main, local, "local-page", 100}, {child, remote, "large-child", 100000}} {
		diff := fmt.Sprintf("--- a/large.txt\n+++ b/large.txt\n@@ -0,0 +1,%d @@\n", capture.rows) + strings.Repeat(line, capture.rows)
		capture.f.publish(t, capture.id, capture.correlation, capture.correlation+"-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{{AfterPath: "large.txt", Diff: diff}}})
	}
	for _, selection := range []struct{ ids, diagnostic string }{
		{local + " " + native, ""},
		{local + " " + first, "ambiguous"}, {remote + " amber999", "never allocated"},
	} {
		ids := selection.ids
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+ids+" --max-tokens 32")
		const marker = "read: incomplete; next_call: mread "
		_, next, found := strings.Cut(stderr, marker)
		if status != 1 || stdout == "" || !found || !strings.Contains(stderr, selection.diagnostic) {
			t.Fatalf("paged %s read failed: stdout bytes=%d stderr=%q status=%d", ids, len(stdout), stderr, status)
		}
		cursor := strings.Fields(next)[0]
		stdout, stderr, status = run("main", mainWorkspace, "mread "+cursor+" --stdout --max-tokens 400")
		if status != 1 || !strings.Contains(stdout, "evidence") || !strings.HasPrefix(stderr, marker) {
			t.Fatalf("paged %s recovery failed: stdout bytes=%d stderr=%q status=%d", ids, len(stdout), stderr, status)
		}
	}
	assertSiblingUnretained()
	// The local ID's number exceeds every child's allocation in that stream.
	localMutation := main.reserve(t, "main", "local-mutation")
	localPath := filepath.Join(mainWorkspace, "local.txt")
	main.publish(t, localMutation, "local-mutation", "local-mutation-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(localPath, localPath, "before\n", "after\n")}})
	writeTestFile(t, localPath, "after\n")
	_, stderr, status = run("main", mainWorkspace, "mchanges revert "+localMutation)
	if status != 0 || stderr != "" || readTestFile(t, localPath) != "before\n" {
		t.Fatalf("noncolliding local mutation: %q %d", stderr, status)
	}
	for _, damage := range []string{"missing", "corrupt", "missing main", "corrupt main"} {
		t.Run(damage+" journal identity", func(t *testing.T) {
			// Lose both launched identities so neither colliding child can be
			// silently omitted in favor of Main's evidence after worker restart.
			owners := []struct{ workspace, thread string }{{childWorkspace, "child"}, {sibling.Cwd, "sibling"}}
			if strings.HasSuffix(damage, "main") {
				// The coordinator locator must survive loss of every journal.
				owners = append(owners, struct{ workspace, thread string }{mainWorkspace, "main"})
			}
			for _, owner := range owners {
				path := filepath.Join(replay.directory, journalFilename(owner.workspace, owner.thread))
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { writeTestFile(t, path, string(data)) })
				if strings.HasPrefix(damage, "missing") {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					writeTestFile(t, path, "{")
				}
			}
			stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first)
			if status == 0 || !strings.Contains(stdout, "main.txt") || !strings.Contains(stderr, "authority is unavailable") {
				t.Errorf("unresolved child read: %q %q %d", stdout, stderr, status)
			}
			for _, operation := range []string{"apply", "revert"} {
				_, stderr, status := run("main", mainWorkspace, "mchanges "+operation+" "+first)
				if status == 0 || !strings.Contains(stderr, "authority is unavailable") || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "before\n" {
					t.Errorf("unresolved child %s: %q %d", operation, stderr, status)
				}
			}
		})
	}
	t.Run("invalid indexes", func(t *testing.T) {
		for _, owner := range []struct{ workspace, thread string }{{childWorkspace, "child"}, {sibling.Cwd, "sibling"}} {
			path := filepath.Join(replay.directory, changeIndexName(owner.workspace, owner.thread))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { writeTestFile(t, path, string(data)) })
			// Decoding succeeds, but the amber allocation has no matching ID.
			writeTestFile(t, path, fmt.Sprintf(`{"Version":1,"Workspace":%q,"Namespace":%q,"Streams":[{"Thread":%q,"Next":1}],"Changes":{}}`, owner.workspace, owner.thread, owner.thread))
		}
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+first)
		if status == 0 || !strings.Contains(stdout, "main.txt") || !strings.Contains(stderr, childWorkspace) || !strings.Contains(stderr, sibling.Cwd) || !strings.Contains(stderr, "counter does not match") || strings.Contains(stderr, "ambiguous") {
			t.Errorf("invalid child indexes suppressed valid caller evidence: %q %q %d", stdout, stderr, status)
		}
		path := filepath.Join(replay.directory, changeIndexName(mainWorkspace, "main"))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { writeTestFile(t, path, string(data)) })
		writeTestFile(t, path, "{")
		_, stderr, status = run("main", mainWorkspace, "mchanges apply "+first)
		if status == 0 || !strings.Contains(stderr, "decode change index") || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "before\n" {
			t.Errorf("caller validation must precede child validation and writes: %q %d", stderr, status)
		}
	})
	other := *main
	other.workspace = t.TempDir()
	otherID := other.reserve(t, "main", "other-workspace")
	otherPath := filepath.Join(other.workspace, "other.txt")
	other.publish(t, otherID, "other-workspace", "other-workspace-call", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(otherPath, otherPath, "before\n", "after\n")}})
	writeTestFile(t, otherPath, "after\n")
	otherSelection := otherID + " --workspace " + shellsyntax.Quote(other.workspace)

	manifestPath := filepath.Dir(u.orchestrateThreads["child"].batch.Checkout) + ".json"
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifestPath, "{")
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+otherSelection)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "+after") {
		t.Errorf("second workspace read with corrupt manifest: %q %q %d", stdout, stderr, status)
	}
	_, stderr, status = run("main", mainWorkspace, "mchanges revert "+otherSelection)
	if status != 0 || stderr != "" || readTestFile(t, otherPath) != "before\n" {
		t.Errorf("second workspace revert with corrupt manifest: %q %d", stderr, status)
	}
	_, stderr, status = run("main", mainWorkspace, "mchanges apply "+first+" --workspace "+shellsyntax.Quote(mainWorkspace))
	if status != 0 || stderr != "" || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "after\n" {
		t.Fatalf("local apply with corrupt manifest: %q %d", stderr, status)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status = run("main", mainWorkspace, "mchanges "+otherSelection)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "+after") {
		t.Errorf("second workspace read with absent manifest: %q %q %d", stdout, stderr, status)
	}
	_, stderr, status = run("main", mainWorkspace, "mchanges apply "+otherSelection)
	if status != 0 || stderr != "" || readTestFile(t, otherPath) != "after\n" {
		t.Errorf("second workspace apply with absent manifest: %q %d", stderr, status)
	}
	_, stderr, status = run("main", mainWorkspace, "mchanges revert "+first+" --workspace "+shellsyntax.Quote(mainWorkspace))
	if status != 0 || stderr != "" || readTestFile(t, filepath.Join(mainWorkspace, "main.txt")) != "before\n" {
		t.Fatalf("local revert with absent manifest: %q %d", stderr, status)
	}
	writeTestFile(t, manifestPath, string(manifestBytes))
	// Main can edit the child's checkout without owning the child's IDs.
	callerInChild := &mchangesSliceFixture{store: replay, ctx: u.ctx, workspace: childWorkspace, thread: "main"}
	callerID := callerInChild.reserve(t, "main", "caller-in-child")
	callerFiles := []mekugi.ReviewFile{mekugi.RenderReviewFile(file, file, after, before)}
	for i := range 12 {
		path := fmt.Sprintf("caller-%02d.txt", i)
		callerFiles = append(callerFiles, mekugi.RenderReviewFile(path, path, "old\n", "caller result\n"))
	}
	callerInChild.publish(t, callerID, "caller-in-child", "caller-in-child-call", mekugiHistory{ReviewFiles: callerFiles})
	for _, ids := range []string{first, first + " " + callerID} {
		stdout, stderr, status := run("main", mainWorkspace, "mchanges "+ids+selected)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "new child line") || ids != first && !strings.Contains(stdout, "caller result") {
			t.Errorf("child selection beside caller stream: %q %q %d", stdout, stderr, status)
		}
	}
	for _, view := range []string{"--net", "--summary"} {
		command := "mchanges " + callerID + " " + first + selected + " " + view
		stdout, stderr, status := run("main", mainWorkspace, command+" -- file.txt")
		want := ""
		if view == "--net" {
			want = "no net changes in selected captured history\n"
		}
		if status != 0 || stderr != "" || stdout != want {
			t.Errorf("same-workspace cancellation %s: %q %q %d", view, stdout, stderr, status)
		}
		whole, stderr, status := run("main", mainWorkspace, command)
		if status != 0 || stderr != "" || strings.Contains(whole, "file.txt") || !strings.Contains(whole, "caller-11.txt") {
			t.Errorf("same-workspace composition %s: %q %q %d", view, whole, stderr, status)
		}
		stdout, stderr, status = run("main", mainWorkspace, command+" --max-tokens 32")
		var recovered strings.Builder
		recovered.WriteString(stdout)
		const marker = "read: incomplete; next_call: mread "
		if status != 1 || !strings.HasPrefix(stderr, marker) {
			t.Fatalf("missing composed recovery %s: %q %q %d", view, stdout, stderr, status)
		}
		for status != 0 {
			if !strings.HasPrefix(stderr, marker) {
				t.Fatalf("composed recovery %s: %q %q %d", view, stdout, stderr, status)
			}
			cursor := strings.Fields(strings.TrimPrefix(stderr, marker))[0]
			stdout, stderr, status = run("main", mainWorkspace, "mread "+cursor+" --stdout --max-tokens 400")
			recovered.WriteString(stdout)
		}
		if recovered.String() != whole {
			t.Errorf("fresh worker changed composed %s selection", view)
		}
	}
	_, stderr, status = run("main", mainWorkspace, "mchanges revert "+first+selected)
	if status == 0 || !strings.Contains(stderr, "read-only") || readTestFile(t, file) != after+"last\n" {
		t.Errorf("child mutation beside caller stream: %q %d", stderr, status)
	}

}
