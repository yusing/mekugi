package router

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func handleTestSession(t *testing.T, store *mekugiReplayStore, thread, parent, fork string) context.Context {
	t.Helper()
	ctx, release, err := store.beginSession(t.Context(), thread, "shared-routing-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return bindTestHandleScope(t, store, ctx, parent, fork)
}

func bindTestHandleScope(t *testing.T, store *mekugiReplayStore, ctx context.Context, parent, fork string) context.Context {
	t.Helper()
	thread := store.scoped(ctx).session.Thread
	metadata := codexTurnMetadata{ThreadID: thread, ParentThreadID: parent, ForkedFromThreadID: fork}
	if parent != "" {
		metadata.SubagentKind = "thread_spawn"
	}
	ctx, err := store.prepareHandleScope(ctx, metadata)
	if err == nil {
		err = store.retainInput(ctx, "/w", nil, nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestHandleScopeSessionIsolationAndSubagents(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := handleTestSession(t, store, "first", "", "")
	second := handleTestSession(t, store, "second", "", "")
	child := handleTestSession(t, store, "child", "first", "first")
	nested := handleTestSession(t, store, "nested", "child", "child")
	for _, test := range []struct {
		ctx                            context.Context
		thread, output, handle, change string
	}{
		{first, "first", "first output", "amber", "amber1"},
		{second, "second", "second output", "amber", "amber1"},
		{child, "child", "child output", "apple", "apple1"},
		{nested, "nested", "nested output", "arch", "arch1"},
	} {
		id, err := store.putShellOutput(test.ctx, test.output, "", 0)
		if err != nil || id != test.handle {
			t.Fatalf("%s output handle: %s, %v", test.thread, id, err)
		}
		id, err = store.reserveChange(test.ctx, "/w", test.thread, "call-"+test.thread)
		if err != nil || id != test.change {
			t.Fatalf("%s change: %s, %v", test.thread, id, err)
		}
	}
	for _, test := range []struct {
		ctx      context.Context
		id, want string
	}{{first, "amber", "first output"}, {second, "amber", "second output"}, {first, "apple", "child output"}, {nested, "amber", "first output"}} {
		record, err := store.readShellOutput(test.ctx, test.id)
		if err != nil || record.Stdout != test.want {
			t.Fatalf("scoped read: %q, %v; want %q", record.Stdout, err, test.want)
		}
	}
	if _, err := store.readShellOutput(second, "apple"); err == nil {
		t.Fatal("unrelated session accessed child's handle")
	}
	firstCall, _ := store.scoped(first).hpatchCallID("amber")
	secondCall, _ := store.scoped(second).hpatchCallID("amber")
	if firstCall == secondCall {
		t.Fatal("session-local handles collide in replay storage")
	}

	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	// Shell workers restore the namespace without a live root or request metadata.
	resumed, release, err := reopened.beginSession(t.Context(), "nested", "new-route")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ids, err := reopened.allocateHandles(resumed, 1)
	if err != nil || ids[0] != "ash" {
		t.Fatalf("resumed nested allocation: %v, %v", ids, err)
	}
}

func TestHandleScopeCompactionKeepsSession(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := handleTestSession(t, store, "root", "", "")
	id, err := store.putShellOutput(ctx, "before compaction", "", 0)
	if err != nil || id != "amber" {
		t.Fatalf("pre-compaction output: %s, %v", id, err)
	}
	change, err := store.reserveChange(ctx, "/w", "root", "call-root")
	if err != nil || change != "amber1" {
		t.Fatalf("pre-compaction change: %s, %v", change, err)
	}

	// A later turn may omit fork/parent metadata and visible ancestry.
	ctx, err = store.prepareHandleScope(ctx, codexTurnMetadata{ThreadID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.retainInput(ctx, "/w", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := store.putShellOutput(ctx, "after compaction", "", 0); err != nil || got != "apple" {
		t.Fatalf("post-compaction output restarted: %s, %v", got, err)
	}
	if got, err := store.reserveChange(ctx, "/w", "root", "call-later"); err != nil || got != "amber2" {
		t.Fatalf("post-compaction change restarted: %s, %v", got, err)
	}
	record, err := store.readShellOutput(ctx, id)
	if err != nil || record.Stdout != "before compaction" {
		t.Fatalf("pre-compaction handle: %+v, %v", record, err)
	}
}

func TestHandleScopeForkClonesOnceAndKeepsReferences(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := handleTestSession(t, store, "root", "", "")
	id, err := store.putShellOutput(root, "first\nsecond\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.readShellOutput(root, id)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.putReadCursor(root, source, [2]int{6, 0}, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	handles, err := store.allocateHandles(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.scoped(root).hpatchCallID(handles[0])
	if err != nil {
		t.Fatal(err)
	}
	change, err := store.reserveChange(root, "/w", "root", call)
	if err != nil {
		t.Fatal(err)
	}
	edits := []mekugi.FileEdit{{Path: "a.txt", Script: `type "old" "new"`}}
	history := mekugiHistory{
		ChangeID: change, CorrelationID: call, Attempt: 1, ExecutingThread: "root",
		Edits: edits, EvaluatorRejected: true, TranslationError: "rejected",
		RecoveryHandles: handles[1:], RecoveryBinding: recoveryBatchHandlesBinding(edits, handles[1:]),
	}
	if err := store.put(root, "/w", map[string]mekugiHistory{call: history}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"fork", "side"} {
		branch := handleTestSession(t, store, thread, "", "root")
		record, err := store.readShellOutput(branch, cursor)
		if err != nil || record.Source != id {
			t.Fatalf("inherited cursor: %+v, %v", record, err)
		}
		again, err := store.putReadCursor(branch, source, [2]int{6, 0}, "stdout")
		if err != nil || again != cursor {
			t.Fatalf("inherited cursor allocated again: %s, %v", again, err)
		}
		if _, err := store.rejectedEdit(branch, "/w", handles[0]); err != nil {
			t.Fatal(err)
		}
		newID, err := store.putShellOutput(branch, thread, "", 0)
		if err != nil || newID != "atlas" {
			t.Fatalf("clone allocation: %s, %v", newID, err)
		}
		newChange, err := store.reserveChange(branch, "/w", thread, "call-"+thread)
		if err != nil || newChange != "amber2" {
			t.Fatalf("clone change sequence: %s, %v", newChange, err)
		}
	}
	if id, err := store.putShellOutput(root, "root later", "", 0); err != nil || id != "atlas" {
		t.Fatalf("fork changed parent counter: %s, %v", id, err)
	}
	if id, err := store.reserveChange(root, "/w", "root", "root-later"); err != nil || id != "amber2" {
		t.Fatalf("fork changed parent stream: %s, %v", id, err)
	}
	// Repeated metadata must not recopy later parent allocations into the fork.
	for range 2 {
		if _, err := store.putShellOutput(root, "only root", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	branch := handleTestSession(t, store, "fork", "", "root")
	if id, err := store.putShellOutput(branch, "fork later", "", 0); err != nil || id != "beach" {
		t.Fatalf("fork was recloned: %s, %v", id, err)
	}
	record, err := store.readShellOutput(branch, "atlas")
	if err != nil || record.Stdout != "fork" {
		t.Fatalf("parent overwrote fork output: %q, %v", record.Stdout, err)
	}
	if _, err := store.readShellOutput(branch, "birch"); err == nil {
		t.Fatal("fork accessed post-fork parent allocation")
	}
}

func TestHandleScopeConcurrentSubagents(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := handleTestSession(t, store, "root", "", "")
	child := handleTestSession(t, store, "child", "root", "")
	var workers sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]bool)
	for i := range 8 {
		workers.Go(func() {
			ctx := []context.Context{root, child}[i%2]
			writer, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Error(err)
				return
			}
			ids, err := writer.allocateHandles(ctx, 8)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if seen[id] {
					t.Errorf("reused handle %s", id)
				}
				seen[id] = true
			}
		})
	}
	workers.Wait()
	if len(seen) != 64 {
		t.Fatalf("allocated %d unique handles", len(seen))
	}
	if err := os.WriteFile(filepath.Join(store.directory, handleScopeName("root")), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.allocateHandles(child, 1); err == nil {
		t.Fatal("corrupt shared namespace silently reset")
	}
}

func TestHandleScopeRouterAndShell(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	root, _ := prepareActivityTest(t, proxy, "same-route", "root", "", "/root", nil)
	other, _ := prepareActivityTest(t, proxy, "same-route", "other", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child-route", "child", "root", "/root/child", nil)
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	manifest.ReplayDirectory = store.directory
	shell, _ := registry.contribution("shell")
	workspace := t.TempDir()
	for _, test := range []struct {
		transform *mekugiResponseTransform
		want      string
	}{{root, "amber1"}, {other, "amber1"}, {child, "apple1"}} {
		thread := test.transform.shellThreadID
		path := thread + ".txt"
		if err := os.WriteFile(filepath.Join(workspace, path), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
		// Match the standalone worker: only its stable thread survives, not the
		// router request context or live activity tree.
		worker, err := shellOutputStore(manifest)
		if err != nil {
			t.Fatal(err)
		}
		ctx, release, err := worker.beginSession(t.Context(), thread, "")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		script := "hpatch " + path + " " + shellQuoteArgument(`type "old" "`+thread+`"`) + "; hchanges " + test.want + " --history"
		result, err := executeShellTool(ctx, manifest, registry.RuntimeRoot, &shell, []string{"bash", script}, nil,
			workspace, append(os.Environ(), "CODEX_THREAD_ID="+thread), nil, nil, nil)
		if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "change "+test.want+"\n") || !strings.Contains(result.Stdout, path) {
			t.Fatalf("%s shell scope: %+v, %v", thread, result, err)
		}
		for _, foreign := range []string{"root.txt", "other.txt", "child.txt"} {
			if foreign != path && strings.Contains(result.Stdout, foreign) {
				t.Fatalf("%s read foreign change %s: %s", thread, foreign, result.Stdout)
			}
		}
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"root": true, "child": true}}})
	if err != nil || len(files) != 2 {
		t.Fatalf("scoped live diff: %+v, %v", files, err)
	}
}

func TestHandleScopeMalformedAuthorDoesNotMisbindChild(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	root, _ := prepareActivityTest(t, proxy, "root-route", "root", "", "/root", nil)
	metadata, ok := decodeCodexTurnMetadata(http.Header{codexTurnMetadataHeader: {
		`{"request_kind":"turn","thread_id":"child","parent_thread_id":"root","subagent_kind":"thread_spawn","agent_name":null}`,
	}})
	if !ok || !metadata.activityIdentityInvalid {
		t.Fatal("fixture did not contain malformed auxiliary author")
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}, "tools": []any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	child, err := proxy.prepareRequest(t.Context(), &request, "child-route", "child", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	ids, err := store.allocateHandles(child.ctx, 1)
	if err != nil || ids[0] != "amber" {
		t.Fatalf("child allocation: %v, %v", ids, err)
	}
	ids, err = store.allocateHandles(root.ctx, 1)
	if err != nil || ids[0] != "apple" {
		t.Fatalf("malformed author detached child's namespace: %v, %v", ids, err)
	}
	child.Close()
	proxy.replayStore, err = openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	corrected, _ := prepareActivityTest(t, proxy, "new-route", "child", "root", "/root/worker", nil)
	ids, err = proxy.replayStore.allocateHandles(corrected.ctx, 1)
	if err != nil || ids[0] != "arch" {
		t.Fatalf("corrected metadata lost the shared counter: %v, %v", ids, err)
	}
}

func TestHandleScopeForkCanReclaimStorageDuringValidation(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := handleTestSession(t, store, "root", "", "")
	id, err := store.putShellOutput(root, "inherited output", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	old, releaseOld := retentionTestSession(t, store, "old", 0)
	visible := mekugiHistory{ToolName: "shell", Script: "true"}
	if err := store.put(old, "/w", map[string]mekugiHistory{
		"visible": visible, "garbage": {ToolName: "shell", Script: strings.Repeat("x", 65536)},
	}); err != nil {
		t.Fatal(err)
	}
	releaseOld()
	retentionTestAge(t, store, "old", time.Hour)
	fork, releaseFork := retentionTestSession(t, store, "fork", 0)
	defer releaseFork()
	fork, err = store.prepareHandleScope(fork, codexTurnMetadata{ThreadID: "fork", ForkedFromThreadID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	sizes, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes = 0
	for _, size := range sizes {
		store.maxBytes += size
	}
	releaseSnapshot, err := store.lockStorageSnapshot(fork)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSnapshot()
	if err := store.retainInput(fork, "/w", nil, map[string]mekugiHistory{"visible": visible}, releaseSnapshot); err != nil {
		t.Fatalf("fork could not reclaim inactive storage: %v", err)
	}
	if record, err := store.readShellOutput(fork, id); err != nil || record.Stdout != "inherited output" {
		t.Fatalf("clone lost inherited reference: %+v, %v", record, err)
	}
	retentionTestExists(t, store, "/w", "visible", true)
	retentionTestExists(t, store, "/w", "garbage", false)
}
