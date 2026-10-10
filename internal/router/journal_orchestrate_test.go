package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJournalOrchestrateCrossCheckout(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, request := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"initial"}}`)
	proxy, workspace := u.proxy, u.session.cwd
	childWorkspace := u.orchestrateThreads["child"].batch.Cwd
	if _, err := proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), Agent: "/root/batch", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	mainSink := proxy.journals.attachNative(workspace, "main")
	defer proxy.journals.detachNative(mainSink)
	if err := proxy.journals.restoreNative(u.ctx, replay, mainSink); err != nil {
		t.Fatal(err)
	}
	childCtx, release, err := replay.beginSession(t.Context(), "child", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := proxy.applyJournal(childCtx, childWorkspace, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Child result"), State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	mainSink.mu.Lock()
	mounted := mainSink.mounted
	mainSink.mu.Unlock()
	if mounted == nil || !slices.ContainsFunc(mounted.Items, func(item journalItem) bool { return item.Title == "Child result" }) {
		t.Fatal("cross-checkout write did not refresh Main's mounted view")
	}
	if err := proxy.journals.observeLifecycle(childCtx, replay, childWorkspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err == nil {
		t.Fatal("working cross-checkout mount released Main's completion gate")
	}
	workerCtx, releaseWorker, err := replay.beginSession(t.Context(), "worker", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWorker()
	if err := proxy.journals.initialize(workerCtx, replay, childWorkspace, "worker", "/root/worker", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(workerCtx, replay, childWorkspace, "worker", "child", "/root/worker", true); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.applyJournal(workerCtx, childWorkspace, "worker", "", []journalMutation{{Op: "log", Text: new("Native result")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.applyJournal(childCtx, childWorkspace, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate native result"), Agent: "/root/worker", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(workerCtx, replay, childWorkspace, "worker", "working", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.applyJournal(childCtx, childWorkspace, "child", "", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err == nil {
		t.Fatal("batch completed with an active native descendant")
	}
	if err := proxy.journals.observeLifecycle(workerCtx, replay, childWorkspace, "worker", "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.applyJournal(childCtx, childWorkspace, "child", "", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
		t.Fatal("finished native descendant still gated its batch task", err)
	}
	// A confirmed sibling is visible to Main, never through a child's ancestor read.
	run := *u.orchestrateThreads["child"].command
	store := proxy.orchestration.store
	sibling, err := store.Prepare(t.Context(), workspace, "main", "sibling")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginLaunch(t.Context(), workspace, "main", "sibling", "work", jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordThread(t.Context(), workspace, "main", "sibling", "sibling", jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	siblingCtx, releaseSibling, err := replay.beginSession(t.Context(), "sibling", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSibling()
	if err := proxy.journals.initialize(siblingCtx, replay, sibling.Cwd, "sibling", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(siblingCtx, replay, sibling.Cwd, "sibling", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindRun(siblingCtx, replay, sibling.Cwd, "sibling", journalRun{Directory: store.Directory, Workspace: run.workspace, Main: run.main}); err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore() // No live ancestry is available after this point.
	serverWire, clientWire := mcp.NewInMemoryTransports()
	server, err := newJournalMCPServer(proxy).Connect(t.Context(), serverWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	read := func(thread, agent, view string, wantError bool) []journalNode {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "journal_read", Arguments: map[string]any{"agent": agent, "view": view}, Meta: mcp.Meta{"threadId": thread, "sessionId": "read-session"}})
		if err != nil || result.IsError != wantError {
			t.Fatalf("journal MCP: %+v, %v", result, err)
		}
		var value struct {
			Nodes []journalNode `json:"nodes"`
		}
		if !wantError {
			encoded, _ := json.Marshal(result.StructuredContent)
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
		}
		return value.Nodes
	}
	if node, ok := mountFind(read("main", "", "combined", false), "/1/@child/1"); !ok || node.Title != "Child result" || node.Author != "/root/batch" {
		t.Fatal("run mount missing after fresh MCP restart", node)
	}
	if _, ok := mountFind(read("main", "batch/worker", "own", false), "/1"); !ok {
		t.Fatal("native descendant missing from run ancestry")
	}
	mainCatalog, err := replay.readRetainedSession(storageSessionName("main"))
	if err != nil || !mainCatalog.Files[journalFilename(childWorkspace, "child")] || !mainCatalog.Files[journalFilename(childWorkspace, "worker")] {
		t.Fatal("Main received cross-checkout references before retaining their records", mainCatalog, err)
	}
	if _, ok := mountFind(read("child", "main", "combined", false), "/@agents/@sibling"); ok {
		t.Fatal("ancestor read exposed sibling")
	}
	if _, ok := mountFind(read("child", "/root/worker", "own", false), "/1"); !ok {
		t.Fatal("batch lost native local descendant selectors")
	}
	read("child", "sibling", "own", true)
	read("child", "/rootworker", "own", true)
	childCatalog, err := replay.readRetainedSession(storageSessionName("child"))
	if err != nil || childCatalog.Files[journalFilename(sibling.Cwd, "sibling")] {
		t.Fatal("child adopted an unauthorized sibling record", childCatalog, err)
	}
	read("main", "unrelated", "own", true)
	if node, ok := mountFind(read("child", "", "own", false), "/1"); !ok || node.Author != "/root" {
		t.Fatal("child's stored root identity changed", node)
	}
	summary, err := summaryForTest(t, u.ctx, replay, workspace, "main")
	if err != nil || !strings.Contains(summary.Text, "Agent batch [working]") {
		t.Fatal("recovery lost run ancestry", summary, err)
	}
	if err := proxy.journals.observeLifecycle(childCtx, replay, childWorkspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	if node, ok := mountFind(read("main", "", "combined", false), "/1"); !ok || node.State != "working" {
		t.Fatal("child completion changed Main's integration task", node)
	}
	// An identifiable corrupt sibling cannot block another child's ancestor read.
	siblingJournal, _, err := readThreadJournal(replay, sibling.Cwd, "sibling")
	if err != nil {
		t.Fatal(err)
	}
	siblingJournal.Receipts = nil
	data, err := json.Marshal(&siblingJournal, json.FormatNilMapAsNull(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replay.directory, journalFilename(sibling.Cwd, "sibling")), data, 0600); err != nil {
		t.Fatal(err)
	}
	read("child", "main", "own", false)
	read("child", "/root/worker", "own", false)
	read("main", "", "combined", true)
	// Losing run authority must not widen reads or hide local task IDs.
	path, err := filepath.Glob(filepath.Join(store.Directory, "*", "*.json"))
	if err != nil || len(path) != 1 {
		t.Fatal(path, err)
	}
	if err := os.WriteFile(path[0], []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	read("main", "", "combined", true)
	read("child", "main", "own", true)
	read("child", "", "tasks", false)
}
