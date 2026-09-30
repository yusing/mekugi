package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mountFixture(t *testing.T) (*mekugiProxy, string) {
	t.Helper()
	proxy, workspace := treeTestJournal(t)
	for _, child := range []struct{ thread, author string }{{"child", "/root/child"}, {"sibling", "/root/sibling"}} {
		if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, child.thread, child.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, child.thread, "tree", child.author, true); err != nil {
			t.Fatal(err)
		}
	}
	return proxy, workspace
}

func mountRead(t *testing.T, proxy *mekugiProxy, workspace, caller, agent, path string) []journalNode {
	t.Helper()
	nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, caller, agent, path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func mountFind(nodes []journalNode, path string) (journalNode, bool) {
	for _, node := range nodes {
		if node.Path == path {
			return node, true
		}
		if found, ok := mountFind(node.Children, path); ok {
			return found, true
		}
	}
	return journalNode{}, false
}

func TestJournalMountDurableBoundAndFallbackViews(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Delegated"), State: new("working")},
		journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("Child result")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "sibling", "", []journalMutation{{Op: "log", Text: new("Sibling result")}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore() // Read must recover from durable records, not in-memory spawn state.
	root := mountRead(t, proxy, workspace, "tree", "", "")
	bound := "/1/@child"
	if node, ok := mountFind(root, bound); !ok || node.Agent != "/root/child" || node.State != "working" {
		t.Fatalf("bound mount missing after restart: %+v", root)
	}
	if node, ok := mountFind(root, bound+"/1"); !ok || node.Title != "Child result" {
		t.Fatalf("bound child's note missing: %+v", root)
	}
	if node, ok := mountFind(root, "/@agents/@sibling/1"); !ok || node.Title != "Sibling result" {
		t.Fatalf("fallback Agents child missing: %+v", root)
	}
	if node, ok := mountFind(root, "/1"); !ok || node.State != "working" {
		t.Fatalf("parent task state overwritten by child lifecycle: %+v", root)
	}
	if node, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", bound+"/1"), bound+"/1"); !ok || node.Title != "Child result" {
		t.Fatal("reserved combined path cannot be read")
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "set", P: bound + "/1", Title: new("Tampered")}}); err == nil {
		t.Fatal("parent mutated mounted child subtree")
	}
	childView := mountRead(t, proxy, workspace, "child", "/root", "")
	if _, ok := mountFind(childView, "/@agents/@sibling"); ok {
		t.Fatal("child's ancestor read exposed sibling mount")
	}
	if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "child", "/root/sibling", "", nil, ""); err == nil {
		t.Fatal("child directly read sibling journal")
	}
}

func TestJournalMountBindingValidationAtomic(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("First")}, journalMutation{Op: "add", Kind: "task", Title: new("Second")})
	before := treeSnapshot(t, proxy, workspace)
	for _, binding := range []journalMutation{{Op: "set", P: "/2", Agent: "/root/child/grandchild"}, {Op: "set", P: "/2", Agent: "/root/child"}} {
		batch := []journalMutation{{Op: "set", P: "/1", Agent: "/root/child"}, binding}
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", batch); err == nil {
			t.Fatalf("invalid batch accepted: %+v", batch)
		}
		after := treeSnapshot(t, proxy, workspace)
		if !reflect.DeepEqual(before.Items, after.Items) || !reflect.DeepEqual(before.Events, after.Events) {
			t.Fatal("invalid binding partially committed")
		}
	}
}

func TestJournalMountResumedChildDoesNotBlockUnrelatedWrites(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Delegated"), State: new("working")}, journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")})
	// A follow-up resumes the child under the already completed parent task.
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "log", Text: new("Unrelated result")}, journalMutation{Op: "add", Kind: "task", Title: new("Next")})
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("working")})
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err == nil {
		t.Fatal("recompleting a task accepted an open mounted child")
	}
}

func TestJournalMountCompletionUsesHostLifecycle(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Delegated"), State: new("working")}, journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	// Provider-authored child completion does not itself complete the host lifecycle.
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Provider says done"), State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err == nil {
		t.Fatal("parent accepted done before host completed child")
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err == nil {
		t.Fatal("parent accepted done while child working")
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatalf("parent cannot finish after host completion: %v", err)
	}
	if mount, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", ""), "/1/@child"); !ok || mount.State != "done" {
		t.Fatalf("mount lifecycle was not observed: %+v", mount)
	}
}

func TestJournalHostTurnObservesUnscopedChildLifecycle(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	// Requests without workspace metadata keep the whole tree unscoped, while the
	// frontend still reports its thread cwd.
	for _, thread := range []struct{ thread, parent, author string }{{"tree", "", "/root"}, {"child", "tree", "/root/child"}} {
		if err := proxy.journals.initialize(t.Context(), proxy.replayStore, "", thread.thread, thread.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, "", thread.thread, thread.parent, thread.author, true); err != nil {
			t.Fatal(err)
		}
	}
	treeApply(t, proxy, "", journalMutation{Op: "add", Kind: "task", Title: new("Delegated"), State: new("working")}, journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	for _, method := range []string{"turn/started", "turn/completed"} {
		event := appServerEvent{ThreadID: "child"}
		event.Turn.Status = "completed"
		if err := proxy.observeJournalHostTurn(t.Context(), t.TempDir(), method, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, "", "tree", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatalf("unscoped child completion was not observed: %v", err)
	}
}

func TestNativeJournalMountedAgentEnterOpensChildActivity(t *testing.T) {
	u, _ := newAppServerTestUI()
	j := threadJournal{Version: 2, TreeAuthored: true, Items: []journalItem{{Path: "/@agents", Kind: "context", Title: "Agents"}, {Path: "/@agents/@child", Kind: "task", Title: "/root/child", Agent: "/root/child", State: "working"}}}
	u.journal = &nativeJournalSink{tree: &j}
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	shell := u.shell
	shell.focus, shell.journalOpen = 4, true
	u.journalView.render(&j, 80, 8, false, true, u.view.painter.Theme)
	index := slices.IndexFunc(u.journalView.rows, func(row journalPaneRow) bool { return strings.Contains(row.node.Path, "@child") })
	if index < 0 {
		t.Fatal("mount row not visible")
	}
	u.journalView.selected = index
	if err := shell.journalKey("\r"); err != nil {
		t.Fatal(err)
	}
	if shell.focus != 4 || !shell.journalOpen || u.notice == "" {
		t.Fatal("Enter on an agent without Activity opened another agent's feed")
	}
	shell.agents.apply(activityPaneEvent{Kind: "agents", Agents: []activityPaneAgent{{Name: "/root"}, {Name: "/root/other"}, {Name: "/root/child"}}})
	shell.agents.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/child", Kind: "text", Text: "Child activity"}}})
	if err := shell.journalKey("\r"); err != nil {
		t.Fatal(err)
	}
	if shell.output == nil || shell.focus != 4 || !shell.journalOpen {
		t.Fatalf("Enter did not open child dialog over Journal: focus=%d journal=%v", shell.focus, shell.journalOpen)
	}
	shell.outputKey("\x1b")
	if shell.output != nil || shell.focus != 4 || !shell.journalOpen {
		t.Fatal("dismissing child dialog changed Journal")
	}
}

func TestJournalMountNativeLiveRestoreAndHostCompletion(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integration"), State: new("working")}, journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	before := treeSnapshot(t, proxy, workspace)
	u := newAppServerSessionTestUI(t, workspace)
	u.proxy, u.thread = proxy, "tree"
	u.session.start("tree", workspace)
	u.journal = proxy.journals.attachNative(workspace, "tree")
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "work"}})
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("Live child finding")}}); err != nil {
		t.Fatal(err)
	}
	nodes, err := journalTree(u.journalTreeSnapshot().Items, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child, ok := mountFind(nodes, "/1/@child/1"); !ok || child.Title != "Live child finding" {
		t.Fatal("child write did not refresh Main mount")
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "work", "status": "completed"}})
	proxy.journals.detachNative(u.journal)
	proxy.journals = newJournalStore()
	u.journal = proxy.journals.attachNative(workspace, "tree")
	if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
		t.Fatal(err)
	}
	nodes, err = journalTree(u.journalTreeSnapshot().Items, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child, ok := mountFind(nodes, "/1/@child"); !ok || child.State != "done" {
		t.Fatalf("host completion was not durable: %+v", nodes)
	}
	after := treeSnapshot(t, proxy, workspace)
	if before.Sequence != after.Sequence || !reflect.DeepEqual(before.Items, after.Items) {
		t.Fatal("mounted view rewrote parent record")
	}
	child, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil || child.LiveSeq != 0 || child.FlushSeq != 0 {
		t.Fatal("mounted view acknowledged child events")
	}
}

func TestJournalMountRefreshesOnlyRelatedViews(t *testing.T) {
	proxy, workspace := mountFixture(t)
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "other", "/root", ""); err != nil {
		t.Fatal(err)
	}
	sink := proxy.journals.attachNative(workspace, "tree")
	if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, sink); err != nil {
		t.Fatal(err)
	}
	view := sink.mounted
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "other", "", []journalMutation{{Op: "log", Text: new("Unrelated")}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.acknowledgeTree(t.Context(), proxy.replayStore, workspace, "tree", 0, false); err != nil {
		t.Fatal(err)
	}
	if sink.mounted != view {
		t.Fatal("unrelated or cursor-only write rebuilt the mounted view")
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("Child finding")}}); err != nil {
		t.Fatal(err)
	}
	if sink.mounted == view || !slices.ContainsFunc(sink.mounted.Items, func(item journalItem) bool { return item.Title == "Child finding" }) {
		t.Fatal("descendant write did not refresh the mounted view")
	}
}

func TestJournalMountFailedRefreshDoesNotKeepStaleView(t *testing.T) {
	proxy, workspace := mountFixture(t)
	sink := proxy.journals.attachNative(workspace, "tree")
	sink.mounted = &threadJournal{Items: []journalItem{{Path: "/1", Kind: "task", Title: "Stale", State: "working"}}}
	sink.tree = &threadJournal{Items: []journalItem{{Path: "/1", Kind: "task", Title: "Fresh", State: "done"}}}
	broken := *proxy.replayStore
	broken.directory = t.TempDir() + "/missing"
	proxy.journals.publishMountedViews(&broken, workspace, "tree")
	if sink.mounted == nil || sink.mounted.Items[0].Title != "Fresh" || !slices.ContainsFunc(sink.mounted.Items, func(item journalItem) bool { return item.Path == "/@mount-error" }) {
		t.Fatalf("failed refresh left a stale mounted view: %+v", sink.mounted)
	}
}

func TestJournalMountAuthenticatedCarrierBindingAndRead(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Delegation")})
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	token := proxy.commentary.subscribe(workspace+"\x00session", "mount-call")
	proxy.commentary.bindActivity(token, "tree")
	for _, mutation := range []string{`{"op":"set","p":"/1","agent":"/root/child"}`, `{"op":"read","p":"/1"}`} {
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(mutation)})
		if !matched || err != nil {
			t.Fatalf("carrier %s: %v %s", mutation, err, output.String())
		}
		if strings.Contains(mutation, `"read"`) {
			var result struct {
				Items []journalNode `json:"items"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 2 || result.Items[1].Path != "/1/@child" {
				t.Fatalf("authenticated read did not include mount: %s", output.String())
			}
		}
	}
}

func TestJournalMountForkPreservesFactsNotSourceChildAuthority(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Finished delegation"), State: new("working")}, journalMutation{Op: "set", P: "/1", Agent: "/root/child"})
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")})
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", "tree"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, "fork", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "fork", "", []journalMutation{{Op: "log", Text: new("Fork fact")}}); err != nil {
		t.Fatalf("inherited binding blocked unrelated work: %v", err)
	}
	nodes := mountRead(t, proxy, workspace, "fork", "", "")
	if len(nodes) != 2 || nodes[0].State != "done" || nodes[0].Agent != "" || len(nodes[0].Children) != 0 {
		t.Fatalf("fork copied child authority or lost task state: %+v", nodes)
	}
	if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "fork", "/root/child", "", nil, ""); err == nil {
		t.Fatal("fork acquired source child read authority")
	}
	original := treeSnapshot(t, proxy, workspace)
	if original.Items[0].Agent != "/root/child" {
		t.Fatal("fork changed original binding")
	}
}

func TestJournalMountFailureDoesNotSuppressTerminalOutcome(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, oversized := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%t/oversized=%t", native, oversized), func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				defer transform.Close()
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", Text: new("Parent fact")}}); err != nil {
					t.Fatal(err)
				}
				count := 1
				if oversized {
					count = 16
				}
				for i := range count {
					thread := fmt.Sprintf("child-%d", i)
					if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, thread, "/root/"+thread, ""); err != nil {
						t.Fatal(err)
					}
					if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, thread, transform.shellThreadID, "/root/"+thread, true); err != nil {
						t.Fatal(err)
					}
					if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error {
						if !oversized {
							j.Receipts = nil
							return nil
						}
						for n := range maxJournalItems {
							j.Items = append(j.Items, journalItem{Path: fmt.Sprintf("/%d", n+1), Kind: "note", Title: "Retained child fact"})
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				var sink *nativeJournalSink
				if native {
					sink = proxy.journals.attachNative(workspace, transform.shellThreadID)
					defer proxy.journals.detachNative(sink)
				}
				wire, err := transform.TransformJSON(regressionFinalResponse(t, "mount-failure", "Parent outcome delivered."))
				if err != nil {
					t.Fatalf("mount failure suppressed parent response: %v", err)
				}
				transform.Delivered(wire)
				transform.ReleaseDelivery()
				var card string
				if native {
					for _, p := range sink.snapshot() {
						if p.card != nil {
							card = journalTurnCard(p.card.Journal, p.card.Since, false)
						}
					}
				} else {
					card = regressionCardText(t, wire)
				}
				if !strings.Contains(card, "Parent outcome delivered.") || !strings.Contains(card, "Mounted journals unavailable:") {
					t.Fatalf("fallback lost own outcome or diagnostic: %q", card)
				}
			})
		}
	}
}

func TestJournalMountRenderedCardLimitPreservesOutcome(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", Text: new("Own fact")}}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		thread := fmt.Sprintf("large-child-%d", i)
		if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, thread, "/root/"+thread, ""); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, thread, transform.shellThreadID, "/root/"+thread, true); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error {
			for n := range maxJournalItems {
				j.Items = append(j.Items, journalItem{Path: fmt.Sprintf("/%d", n+1), Kind: "task", State: "pending", Title: strings.Repeat("x", maxJournalItemBytes-256)})
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	wire, err := transform.TransformJSON(regressionFinalResponse(t, "mount-byte-limit", "Outcome survives a large mounted card."))
	if err != nil {
		t.Fatal(err)
	}
	transform.Delivered(wire)
	transform.ReleaseDelivery()
	card := regressionCardText(t, wire)
	if !strings.Contains(card, "Outcome survives a large mounted card.") || !strings.Contains(card, "combined card exceeds terminal capacity") {
		t.Fatalf("missing rendered-size fallback: %q", card)
	}
}
