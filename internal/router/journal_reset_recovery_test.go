package router

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestJournalSummarySeparatesCompletedAgentFromOpenIntegration(t *testing.T) {
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
	for _, want := range []string{"/1 [working] Inspect changes", "bound agent done; integration remains open (retained result: /1/@child)", "/1/@child [done]", "Resume: continue /1"} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("missing %q: %s", want, summary.Text)
		}
	}
	j, _, err := readThreadJournal(store, workspace, "tree")
	if err != nil || j.Items[0].State != "working" {
		t.Fatalf("recovery changed parent state: %+v %v", j, err)
	}
}

func TestJournalSummaryUnconfirmedExecutionsUseRetainedOutcomes(t *testing.T) {
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
	for _, want := range []string{"Execution observations without retained completion:", "2 earlier observations omitted.", "not proof of a running process", "No continuation handle or Code Mode store value is restored"} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("missing %q: %s", want, summary.Text)
		}
	}
	if strings.Count(summary.Text, ": test pending-") != 8 || strings.Contains(summary.Text, "already finished") || strings.Contains(summary.Text, "foreign") {
		t.Fatalf("incorrect pending scope/outcomes: %s", summary.Text)
	}
}

// The reported post-reset source passed journal() directly to allSettled.
// A barrier proves lowering preserves Promise concurrency, not just its value.
func TestJournalCodeModePromiseBatchRecovery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered Code Mode")
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
	for _, source := range []string{`text("journal({op:'read'})")`, `// journal({op:'read'})`, `tools.journal({op:'read'})`, `const broken = ; journal({op:'read'})`} {
		calls, err := findCodeModeCommentaryCalls(source)
		if err != nil || len(calls) != 0 {
			t.Fatalf("instrumented noncall %q: %v %v", source, calls, err)
		}
	}
}

func TestJournalSummaryDoesNotPromoteDescendantTaskState(t *testing.T) {
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
	if strings.Contains(summary.Text, "bound agent done") {
		t.Fatalf("descendant task overrode child lifecycle: %s", summary.Text)
	}
}
