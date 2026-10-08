package router

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestJournalSummarySeparatesCompletedAgentFromOpenIntegration(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Inspect changes"), State: new("working"), Agent: "/root/child"})
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, t.Context(), store, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/1 [working] Inspect changes", "child done; integration remains open", "Agent child [done]; parent task /1", "Resume: no runnable local task"} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("missing %q: %s", want, summary.Text)
		}
	}
	j, _, err := readThreadJournal(store, workspace, "tree")
	if err != nil || j.Items[0].State != "working" {
		t.Fatalf("recovery changed parent state: %+v %v", j, err)
	}
}

func TestJournalSummaryKeepsLatestAgentOutcomeForOpenIntegration(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integrate review"), State: new("working"), Agent: "/root/child"})
	child, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil {
		t.Fatal(err)
	}
	child.Items = append(child.Items,
		journalItem{ID: "/1", Path: "/1", Kind: "answer", Title: "Outcome", Body: "Old reproduction now resolved", Author: child.Author},
		journalItem{ID: "/2", Path: "/2", Kind: "answer", Title: "Outcome", Body: "One unresolved review finding needs integration", Author: child.Author},
	)
	if err := writeThreadJournal(proxy.replayStore, child); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, t.Context(), proxy.replayStore, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "One unresolved review finding needs integration") || strings.Contains(summary.Text, "Old reproduction now resolved") {
		t.Fatalf("open integration lost its latest outcome or revived old findings: %s", summary.Text)
	}
}

func TestJournalSummaryClosedTasksKeepContextPathsNotHistory(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"done", "dropped"} {
		t.Run(state, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			ctx, thread := transform.ctx, transform.shellThreadID
			if _, err := proxy.journals.apply(ctx, proxy.replayStore, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Finished checkpoint"), State: new(state), Reason: new("Scope decision")},
				{Op: "add", Under: "/1", Kind: "context", Title: new("Active protocol constraint"), Body: new("Keep version 1")},
				{Op: "add", Under: "/1", Kind: "note", Title: new("Old validation"), Body: new(strings.Repeat("obsolete test detail ", 100))},
				{Op: "add", Kind: "task", Title: new("Current acceptance"), State: new("working")},
				{Op: "log", P: "/2", Text: new("Runtime acceptance remains unresolved")},
			}); err != nil {
				t.Fatal(err)
			}
			summary, err := summaryForTest(t, ctx, proxy.replayStore, workspace, thread)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Active protocol constraint", "Runtime acceptance remains unresolved", "Resume: continue /2"} {
				if !strings.Contains(summary.Text, want) {
					t.Errorf("missing active fact %q: %s", want, summary.Text)
				}
			}
			if strings.Contains(summary.Text, "obsolete test detail") || strings.Contains(summary.Text, "Finished checkpoint") || strings.Contains(summary.Text, "Keep version 1") {
				t.Fatalf("bodyless closed task expanded its history: %s", summary.Text)
			}
			depth := 1
			nodes, err := proxy.journals.readTree(ctx, proxy.replayStore, workspace, thread, "", "/1", &depth, "own")
			if err != nil {
				t.Fatal(err)
			}
			if note, ok := mountFind(nodes, "/1/2"); !ok || !strings.Contains(note.Body, "obsolete test detail") {
				t.Fatalf("on-demand read lost closed history: %+v", nodes)
			}
		})
	}
}

func TestJournalSummaryUnconfirmedExecutionsUseRetainedOutcomes(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, thread := transform.ctx, transform.shellThreadID
	histories := map[string]mekugiHistory{}
	for i := range 10 {
		call := fmt.Sprintf("pending-%02d", i)
		histories[call] = mekugiHistory{ExecutingThread: thread, Script: "test " + call, ExecObservation: &execObservation{CodeMode: i%2 == 0}}
	}
	for _, codeMode := range []bool{false, true} {
		call := fmt.Sprintf("finished-%t", codeMode)
		histories[call] = mekugiHistory{ExecutingThread: thread, Script: "already finished", ExecObservation: &execObservation{CodeMode: codeMode}}
		histories[execDerivedCallID(call, codeMode)] = mekugiHistory{ExecutingThread: thread, ExecOutcome: &execOutcome{Status: execStatusCompleted}}
	}
	histories["foreign"] = mekugiHistory{ExecutingThread: "other", Script: "foreign command", ExecObservation: &execObservation{}}
	if err := proxy.replayStore.put(ctx, workspace, histories); err != nil {
		t.Fatal(err)
	}
	if err := proxy.replayStore.put(ctx, t.TempDir(), map[string]mekugiHistory{"foreign-workspace": {ExecutingThread: thread, Script: "foreign workspace command", ExecObservation: &execObservation{}}}); err != nil {
		t.Fatal(err)
	}
	restarted, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, restarted, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Execution observations without retained completion:", "2 earlier observations omitted.", "not proof of a running process", "No continuation handle or exec store value is restored"} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("missing %q: %s", want, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "\npending-") {
		t.Fatalf("recovery exposed observation call IDs: %s", summary.Text)
	}
	if strings.Count(summary.Text, "Observed: test pending-") != 8 || strings.Contains(summary.Text, "already finished") || strings.Contains(summary.Text, "foreign") {
		t.Fatalf("incorrect pending scope/outcomes: %s", summary.Text)
	}
}

// The reported post-reset source passed journal() directly to allSettled.
// A barrier proves lowering preserves Promise concurrency, not just its value.
func TestJournalCodeModePromiseBatchRecovery(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered exec")
	}
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			transform, _ := newRuntimeCommentaryTransform(t)
			source := `const results = await Promise.allSettled([journal({op:"read",p:"/4",depth:2}), tools.exec_command({cmd:"sibling"})]); process.stdout.write(JSON.stringify(results));`
			lowered, changed, err := transform.lowerCodeModeCommentary("recovered-batch", source)
			if err != nil || !changed {
				t.Fatalf("lowering: %t %v", changed, err)
			}
			script := `let release; const sibling = new Promise(resolve => {release=resolve;}); let reads=0, siblings=0;
const tools={exec_command:async ({cmd})=>{
 if(cmd==="sibling") { siblings++; release(); return {exit_code:0,output:"sibling completed"}; }
 reads++; await sibling;
 if (` + strconv.FormatBool(failure) + `) return {exit_code:1,output:"unavailable"};
 return {exit_code:0,output:JSON.stringify({ok:true,items:[{path:"/4",kind:"task",title:"Retained task",children:[]}]})};
}};
const timer=setTimeout(()=>{console.error("batch did not complete");process.exit(2)},2000);
(async()=>{` + lowered + `; if(reads!==1||siblings!==1) throw new Error("wrong execution count"); clearTimeout(timer);})().catch(error=>{console.error(error);process.exit(1)});`
			output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
			if err != nil {
				t.Fatalf("lowered batch: %s %v", output, err)
			}
			want := `"status":"fulfilled","value":[{"path":"/4"`
			if failure {
				want = `"status":"rejected"`
			}
			if !strings.Contains(string(output), want) || !strings.Contains(string(output), "sibling completed") {
				t.Fatalf("batch result: %s", output)
			}
		})
	}
}

func TestJournalCodeModeCallRecognitionLeavesNonCallsAlone(t *testing.T) {
	t.Parallel()
	for _, source := range []string{`text("journal({op:'read'})")`, `// journal({op:'read'})`, `tools.journal({op:'read'})`, `const broken = ; journal({op:'read'})`} {
		calls, err := findCodeModeCommentaryCalls(source)
		if err != nil || len(calls) != 0 {
			t.Fatalf("instrumented noncall %q: %v %v", source, calls, err)
		}
	}
}

func TestJournalSummaryDoesNotPromoteDescendantTaskState(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Parent integration"), State: new("working"), Agent: "/root/child"})
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "grandchild", "/root/child/grandchild", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, "grandchild", "child", "/root/child/grandchild", true); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "grandchild", "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Finished descendant task"), State: new("done"), Agent: "/root/child/grandchild"}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, t.Context(), proxy.replayStore, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary.Text, "child done; integration remains open") {
		t.Fatalf("descendant task overrode child lifecycle: %s", summary.Text)
	}
}

// Completed work is omitted, with one shared hint for finding retained paths.
// Those paths still return the complete evidence.
func TestJournalSummaryOmitsFinishedWorkForOnDemandReads(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace,
		journalMutation{Op: "add", Kind: "task", Title: new("Delegate tests"), State: new("working"), Agent: "/root/child"},
		journalMutation{Op: "log", P: "/1", Text: new("PARENT-LOG-DETAIL")},
	)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Still-open child task"), State: new("working")},
		{Op: "log", Text: new("CHILD-LOG-DETAIL")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, "child", func(j *threadJournal, _ bool) error {
		j.Items = append(j.Items, journalItem{ID: "/outcome", Path: "/outcome", Kind: "answer", Title: "Outcome", Body: "CLOSED-INTEGRATION-OUTCOME", Author: j.Author})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace,
		journalMutation{Op: "set", P: "/1", State: new("done"), Body: new("Tests added and passing")},
		journalMutation{Op: "add", Kind: "task", Title: new("Next slice"), State: new("pending")},
	)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "sibling", "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Review"), State: new("done")},
		{Op: "log", P: "/1", Text: new("REVIEW-LOG-DETAIL")},
	}); err != nil {
		t.Fatal(err)
	}
	sibling, _, err := readThreadJournal(proxy.replayStore, workspace, "sibling")
	if err != nil {
		t.Fatal(err)
	}
	sibling.Items = append(sibling.Items,
		journalItem{ID: "/2", Path: "/2", Kind: "answer", Title: "Outcome", Body: "STALE: two defects found", Author: sibling.Author},
		journalItem{ID: "/3", Path: "/3", Kind: "answer", Title: "Outcome", Body: "Both defects resolved", Author: sibling.Author},
	)
	if err := writeThreadJournal(proxy.replayStore, sibling); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "sibling", "done", ""); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, t.Context(), proxy.replayStore, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/1 [done] Delegate tests", "Still-open child task", `journal({op:"read",view:"outline"})`, `journal({op:"read",depth:1})`, "Resume: continue /2",
	} {
		if !strings.Contains(summary.Text, want) {
			t.Errorf("summary missing %q:\n%s", want, summary.Text)
		}
	}
	for _, folded := range []string{"CLOSED-INTEGRATION-OUTCOME", "/@agents/@sibling", "Tests added and passing", "Both defects resolved", "PARENT-LOG-DETAIL", "CHILD-LOG-DETAIL", "REVIEW-LOG-DETAIL", "STALE", "/@agents Agents"} {
		if strings.Contains(summary.Text, folded) {
			t.Errorf("summary kept folded entry %q:\n%s", folded, summary.Text)
		}
	}
	store, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore, proxy.journals = store, newJournalStore()
	agentIndex, err := proxy.journals.readTree(t.Context(), store, workspace, "tree", "", "", new(1), "combined")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mountFind(agentIndex, "/@agents/@sibling"); !ok {
		t.Fatalf("agent-history hint cannot find omitted mount: %+v", agentIndex)
	}
	if node, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", "/@agents/@sibling"), "/@agents/@sibling/3"); !ok || node.Body != "Both defects resolved" {
		t.Fatalf("indexed agent outcome is not readable after restart: %+v", node)
	}
	if node, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", "/1"), "/1"); !ok || node.Body != "Tests added and passing" {
		t.Fatalf("indexed task body is not readable after restart: %+v", node)
	}
}

func TestJournalSummaryOpenSubtaskBoundsFolding(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace,
		journalMutation{Op: "add", Kind: "task", Title: new("Original approach"), State: new("working")},
		journalMutation{Op: "add", Under: "/1", Kind: "task", Title: new("Kept subtask"), State: new("working"), Agent: "/root/child"},
		journalMutation{Op: "log", P: "/1/1", Text: new("OPEN-SUB-PROGRESS")},
		journalMutation{Op: "log", P: "/1", Text: new("DROPPED-DETAIL")},
	)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("CHILD-NOTE")}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("dropped"), Reason: new("Approach replaced"), Body: new("Superseded plan")})
	summary, err := summaryForTest(t, t.Context(), proxy.replayStore, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OPEN-SUB-PROGRESS", "Agent child [done]; parent task /1/1", "/1 [dropped] Original approach; Approach replaced"} {
		if !strings.Contains(summary.Text, want) {
			t.Errorf("summary missing %q:\n%s", want, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "DROPPED-DETAIL") || strings.Contains(summary.Text, "Superseded plan") || strings.Contains(summary.Text, "CHILD-NOTE") {
		t.Errorf("dropped task kept its own folded note:\n%s", summary.Text)
	}
}

func TestJournalSummaryAgentSelectorsUseLocalPaths(t *testing.T) {
	proxy, workspace := mountFixture(t)
	ctx, store := t.Context(), proxy.replayStore
	if err := proxy.journals.initialize(ctx, store, workspace, "nested-child", "/root/child/child", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(ctx, store, workspace, "nested-child", "child", "/root/child/child", true); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Main integration"), State: new("working"), Agent: "/root/child"})
	for _, target := range []struct{ thread, agent, title, delegate string }{
		{"child", "child", "Child work", "/root/child/child"},
		{"nested-child", "child/child", "Nested work", ""},
	} {
		if _, err := proxy.journals.apply(ctx, store, workspace, target.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: &target.title, State: new("working"), Agent: target.delegate, Body: new("Retain 日本語 verbatim")}}); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.observeLifecycle(ctx, store, workspace, target.thread, "working", ""); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, reopened, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "Retain 日本語 verbatim") {
		t.Fatal("recovery changed authored Unicode content")
	}
	for _, forbidden := range []string{"/root/", "/@", "·", "…", "**"} {
		if strings.Contains(summary.Text, forbidden) {
			t.Errorf("recovery exposes formatting noise %q", forbidden)
		}
	}
	for _, target := range []struct{ agent, title string }{{"child", "Child work"}, {"child/child", "Nested work"}} {
		if strings.Count(summary.Text, "Agent "+target.agent+" [working]") != 1 || !strings.Contains(summary.Text, "/1 [working] "+target.title) {
			t.Fatalf("agent heading/local path missing or repeated: %s", summary.Text)
		}
		nodes, err := newJournalStore().readTree(ctx, reopened, workspace, "tree", target.agent, "/1", new(0), "own")
		if err != nil || len(nodes) != 1 || nodes[0].Title != target.title || nodes[0].Body != "Retain 日本語 verbatim" {
			t.Fatalf("agent/local-path hint did not read its owner: %+v %v", nodes, err)
		}
	}
}
