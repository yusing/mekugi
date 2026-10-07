package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func runJournalSurveyCell(t *testing.T, setup, lowered, verify string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered exec")
	}
	script := `const assert = require("node:assert/strict");` + setup + `
(async()=>{` + lowered + `})().then(async result=>{` + verify + `}, error=>{throw error;}).catch(error=>{console.error(error);process.exitCode=1;});`
	if output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("lowered cell: %v\n%s", err, output)
	} else if len(output) != 0 {
		t.Log(strings.TrimSpace(string(output)))
	}
}

func lowerJournalSurveyCell(t *testing.T, transform *mekugiResponseTransform, source string) string {
	t.Helper()
	lowered, changed, err := transform.lowerCodeModeCommentary("survey-call", source)
	if err != nil || !changed {
		t.Fatalf("lowering: changed=%t err=%v", changed, err)
	}
	return lowered
}

func TestJournalSurveySharedHelperConcurrentNestedYieldedCalls(t *testing.T) {
	t.Parallel()
	transform, proxy := newRuntimeCommentaryTransform(t)
	source := `// await journal({op:"log",text:"not executable"});
const literal = "await journal({op:'list'})";
let evaluations = 0;
const argument = () => { evaluations++; return {op:"plan",tasks:["First","Second"]}; };
const [read, list, set] = await Promise.all([
  (async()=>await journal({op:"read",p:"/1"}))(),
  (async()=>await journal({op:"list"}))(),
  (async()=>await journal({op:"set",p:(await journal(argument()))[1],state:"working"}))()
]);
return {read,list,set,evaluations,literal};
await journal({op:"log",text:"unreachable"});`
	lowered := lowerJournalSurveyCell(t, transform, source)
	prefix := workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, runtimeCommentaryToken(t, transform)})
	setup := `const prefix = ` + strconv.Quote(prefix) + `;
const receipts = []; globalThis.text = value => receipts.push(value);
const pending = new Map(); const counts = new Map(); const sessions = []; let nextSession = 100;
const revision = {read:"a".repeat(64),list:"b".repeat(64)};
const tools = {
 exec_command: async ({cmd,login}) => {
  assert.equal(login,false); assert.ok(cmd.startsWith(prefix+" '"),"authenticated prefix changed");
  const match = cmd.slice(prefix.length).match(/^ '([^']*)'(.*)$/); assert.ok(match);
  const mutation = JSON.parse(decodeURIComponent(match[1]));
  const count = (counts.get(mutation.op)||0)+1; counts.set(mutation.op,count);
  let publication;
  if (mutation.op === "read" || mutation.op === "list") {
   assert.equal(match[2], count===1 ? "" : " 1 "+revision[mutation.op]);
   assert.ok(count<=2);
   const items = mutation.op === "read"
    ? [{path:count===1?"/1":"/1/1",kind:"task",title:count===1?"Parent":"Child",children:[]}]
    : [{path:count===1?"/8":"/9",title:"List "+count}];
   publication={ok:true,items}; if(count===1) Object.assign(publication,{next:1,revision:revision[mutation.op]});
  } else if (mutation.op === "plan") {
   assert.equal(count,1); assert.deepEqual(mutation.tasks,["First","Second"]); publication={ok:true,items:["/2","/3"]};
  } else {
   assert.equal(mutation.op,"set"); assert.equal(mutation.p,"/3"); assert.equal(count,1); publication={ok:true,items:["/3"]};
  }
  const session_id=nextSession++; const output=JSON.stringify(publication); const split=Math.floor(output.length/2);
  pending.set(session_id,output.slice(split)); sessions.push(session_id);
  await new Promise(resolve=>setImmediate(resolve));
  return {session_id,output:output.slice(0,split)};
 },
 write_stdin: async ({session_id,chars,yield_time_ms}) => {
  assert.equal(chars,""); assert.equal(yield_time_ms,10000); assert.ok(pending.has(session_id));
  const output=pending.get(session_id); pending.delete(session_id);
  await new Promise(resolve=>setImmediate(resolve)); return {exit_code:0,output};
 }
};`
	verify := `assert.equal(result.evaluations,1); assert.equal(result.set,"/3");
assert.equal(result.literal,"await journal({op:'list'})");
assert.equal(result.read.length,1); assert.equal(result.read[0].path,"/1");
assert.deepEqual(result.read[0].children.map(n=>n.path),["/1/1"]);
assert.deepEqual(result.list.map(n=>n.path),["/8","/9"]);
assert.deepEqual(receipts,['journal paths: ["/2","/3"]']);
assert.equal(pending.size,0); assert.equal(new Set(sessions).size,6);
assert.deepEqual(Object.fromEntries(counts),{read:2,list:2,plan:1,set:1});
console.log("Observed stock calls: exec_command="+sessions.length+", write_stdin="+sessions.length);`
	runJournalSurveyCell(t, setup, lowered, verify)
}

func TestJournalSurveyLoweringPayloadGrowth(t *testing.T) {
	t.Parallel()
	transform, _ := newRuntimeCommentaryTransform(t)
	one := lowerJournalSurveyCell(t, transform, `await journal({op:"read",view:"tasks"});`)
	many := lowerJournalSurveyCell(t, transform, strings.Repeat(`await journal({op:"read",view:"tasks"});`, 20))
	t.Logf("Lowered payload: one call=%d bytes, twenty calls=%d bytes, growth=%d bytes", len(one), len(many), len(many)-len(one))
	// An additional call should cost a compact invocation, not another copy of
	// the stock transport, continuation, and reconstruction implementation.
	if growth := len(many) - len(one); growth > 19*200 {
		t.Fatalf("19 additional journal calls grew the cell by %d bytes, want at most %d", growth, 19*200)
	}
}

// Created plan and add paths must reach the model, which cannot otherwise
// address a new node without reading the tree back. Logs and edits stay quiet.
func TestJournalSurveyBatchReturnValuesAndCreationReceipts(t *testing.T) {
	t.Parallel()
	transform, _ := newRuntimeCommentaryTransform(t)
	lowered := lowerJournalSurveyCell(t, transform, `const plain = await journal([{op:"log",text:"Finding"}]);
const plan = await journal([{op:"plan",tasks:["Work"]},{op:"log",text:"Evidence"}]);
const single = await journal({op:"log",text:"Single"});
const added = await journal({op:"add",kind:"context",title:"Constraint"});
const edited = await journal([{op:"set",p:"/5",body:"Revised"},{op:"add",title:"Fact"}]);
const quiet = await journal({op:"set",p:"/5",superseded_by:"/2"});
return {plain,plan,single,added,edited,quiet};`)
	setup := `const receipts=[]; globalThis.text=value=>receipts.push(value); let calls=0;
const tools={exec_command:async()=>({exit_code:0,output:JSON.stringify({ok:true,items:[["/1"],["/2","/3"],["/4"],["/5"],["/5","/6"],["/5"]][calls++]})})};`
	runJournalSurveyCell(t, setup, lowered, `assert.deepEqual(result,{plain:["/1"],plan:["/2","/3"],single:"/4",added:"/5",edited:["/5","/6"],quiet:"/5"});
assert.deepEqual(receipts,['journal paths: ["/2","/3"]','journal paths: ["/5"]','journal paths: ["/5","/6"]']); assert.equal(calls,6);`)
}

func TestJournalSurveyOperationFailuresKeepTransportDetail(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"read", "list", "mutation"} {
		for _, failure := range []string{"exit", "throw"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				transform, _ := newRuntimeCommentaryTransform(t)
				op := operation
				if op == "mutation" {
					op = "log"
				}
				source := `try { await journal({op:` + strconv.Quote(op) + `,text:"finding"}); } catch (error) { return error.message; } throw new Error("missing failure");`
				lowered := lowerJournalSurveyCell(t, transform, source)
				setup := `const tools={exec_command:async()=>{` + `throw new Error("socket disconnected: fixture detail");` + `}};`
				want := "journal " + operation + " failed: socket disconnected: fixture detail"
				if failure == "exit" {
					setup = `const tools={exec_command:async()=>({session_id:42,output:"permission "}),write_stdin:async ({session_id})=>{assert.equal(session_id,42);return {exit_code:7,output:"denied: fixture detail"};}};`
					want = "journal " + operation + " failed: transport exited 7: permission denied: fixture detail"
				}
				runJournalSurveyCell(t, setup, lowered, `assert.equal(result,`+strconv.Quote(want)+`);`)
			})
		}
	}
}

func TestJournalSurveySelectorFailureThroughPublisher(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "nested", "/root/child/nested", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, "nested", "child", "/root/child/nested", true); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"/root/child/nested", "child/nested"} {
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", selector, "", nil, "own"); err != nil {
			t.Fatalf("canonical/root-omitted selector %q: %v", selector, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	token := proxy.commentary.subscribe(workspace+"\x00session", "selector-call")
	proxy.commentary.bindActivity(token, "child")
	var output bytes.Buffer
	matched, publicationErr := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"read","agent":"nested","view":"own"}`)})
	if !matched || publicationErr == nil {
		t.Fatalf("leaf-relative selector publication: matched=%t err=%v output=%s", matched, publicationErr, output.String())
	}
	transform, _ := newRuntimeCommentaryTransform(t)
	lowered := lowerJournalSurveyCell(t, transform, `try { await journal({op:"read",agent:"nested",view:"own"}); } catch(error) { return error.message; } throw new Error("leaf-relative selector accepted");`)
	setup := `const tools={exec_command:async()=>({exit_code:1,output:` + strconv.Quote(publicationErr.Error()+"\n"+output.String()) + `})};`
	runJournalSurveyCell(t, setup, lowered, `assert.ok(result.startsWith("journal read failed: ")); assert.ok(result.includes('"/root/nested"')); assert.ok(result.includes("canonical /root/... path (or omit /root/)")); assert.ok(result.includes("not a leaf name")); assert.ok(result.includes("omit agent"));`)
}
