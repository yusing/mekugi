package router

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestJournalHelperFinishBatchPersistsOnlyAcceptedReceipt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		mutations []journalMutation
		want      bool
	}{
		{"finish only", []journalMutation{{Op: "finish"}}, true},
		{"final report", []journalMutation{{Op: "add", Title: new("Checks passed")}, {Op: "finish"}}, true},
		{"finish is not last", []journalMutation{{Op: "finish"}, {Op: "add", Title: new("must roll back")}}, false},
		{"unsupported finish operand", []journalMutation{{Op: "add", Title: new("must roll back")}, {Op: "finish", SupersededBy: new("/1")}}, false},
		{"rejected mutation", []journalMutation{{Op: "set", P: "/missing", State: new("done")}, {Op: "finish"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			transform.shellTurnID = "final-turn"
			token := testRuntimeCommentaryCall(t, transform, "final-work")
			body, err := json.Marshal(map[string]any{"journal": test.mutations, "id": "publication"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			proxy.commentary.serveHTTP(response, request)
			var outcome struct {
				OK    bool     `json:"ok"`
				Items []string `json:"items"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &outcome) != nil || outcome.OK != test.want {
				t.Fatalf("publication = %d %s", response.Code, response.Body)
			}
			if test.want && outcome.Items == nil {
				t.Fatal("finish must return an empty path array, not null")
			}
			found := false
			if err := proxy.journals.transaction(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, func(j *threadJournal, exists bool) error {
				_, found = j.Receipts["runtime:"+journalHostFinishReceipt(transform.shellTurnID, "final-work")]
				if !test.want && len(j.Items) != 0 {
					t.Fatalf("rejected completion leaked journal mutations: %+v", j.Items)
				}
				return errJournalUnchanged
			}); err != nil {
				t.Fatal(err)
			}
			if found != test.want {
				t.Fatalf("finish receipt = %v, want %v", found, test.want)
			}
		})
	}
}

func TestJournalFinishCandidateTransaction(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, tasks, reset, state   string
		stopped, child, stale, want bool
	}{
		{name: "pending", tasks: `"Work"`},
		{name: "working", tasks: `{"title":"Work","state":"working"}`},
		{name: "nested", tasks: `{"title":"Work","tasks":[{"title":"Nested"}]}`},
		{name: "completes candidate", tasks: `"Work"`, state: "done", want: true},
		{name: "blocked report", tasks: `"Work"`, state: "blocked", want: true},
		{name: "slice boundary", tasks: `{"title":"First","state":"working"},"Second"`, reset: "slice", state: "done", want: true},
		{name: "old slice boundary", tasks: `{"title":"First","state":"done"},"Second"`, reset: "slice", stale: true},
		{name: "user stopped", tasks: `"Work"`, stopped: true, want: true},
		{name: "child local work", tasks: `"Work"`, child: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			var tasks []jsontext.Value
			if err := json.Unmarshal([]byte(`[`+test.tasks+`]`), &tasks); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "plan", Reset: test.reset, Tasks: tasks}}); err != nil {
				t.Fatal(err)
			}
			transform.shellTurnID = "finish-turn"
			if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, transform.shellTurnID); err != nil {
				t.Fatal(err)
			}
			if test.stopped {
				if err := proxy.journals.stopJournalTurn(t.Context(), proxy.replayStore, workspace, thread, transform.shellTurnID); err != nil {
					t.Fatal(err)
				}
			}
			if test.child {
				if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error { j.Parent = "parent"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			transform.commentaryTools = commentaryToolCatalog{functionToolKey("functions", "exec_command"): {qualifiedName: "functions.exec_command"}}
			before, _, err := readThreadJournal(proxy.replayStore, workspace, thread)
			if err != nil {
				t.Fatal(err)
			}
			mutations := []journalMutation{{Op: "log", Text: new("Final report")}}
			if test.state != "" {
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", State: &test.state, Reason: new("Needs input")})
			}
			if !test.want {
				candidate := append(slices.Clone(mutations), journalMutation{Op: "finish", finishTurn: transform.shellTurnID})
				_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "candidate", candidate)
				path := "/1"
				if test.name == "nested" {
					path = "/1/1"
				}
				if test.stale {
					path = "/2"
				}
				if err == nil || !strings.Contains(err.Error(), "runnable task "+path+" (") {
					t.Fatalf("missing task correction: %v", err)
				}
				rejected, _, readErr := readThreadJournal(proxy.replayStore, workspace, thread)
				if readErr != nil || !reflect.DeepEqual(before.Items, rejected.Items) || !reflect.DeepEqual(before.Events, rejected.Events) || before.Sequence != rejected.Sequence || before.NextID != rejected.NextID || !reflect.DeepEqual(before.Receipts, rejected.Receipts) {
					t.Fatalf("rejected finish changed journal: %v", readErr)
				}
			}
			mutations = append(mutations, journalMutation{Op: "finish"})
			var item map[string]jsonv1.RawMessage
			if err := json.Unmarshal(mustMarshalJSON(map[string]any{"type": "function_call", "namespace": "functions", "name": "exec_command", "call_id": "final-work", "arguments": string(mustMarshalJSON(map[string]any{"cmd": "true", "journal": mutations}))}), &item); err != nil {
				t.Fatal(err)
			}
			// A native array cannot return a correctable rejection, so it keeps
			// its other mutations and leaves the host result to the provider.
			if _, err := transform.transformStructuredCommentary(item); err != nil {
				t.Fatalf("native finish failed the response: %v", err)
			}
			after, _, readErr := readThreadJournal(proxy.replayStore, workspace, thread)
			if readErr != nil {
				t.Fatal(readErr)
			}
			_, receipt := after.Receipts["runtime:"+journalHostFinishReceipt(transform.shellTurnID, "final-work")]
			_, applied := after.Receipts["final-work:journal"]
			if receipt != test.want || applied == test.want {
				t.Fatalf("finish receipt=%v mutation receipt=%v, want finish=%v", receipt, applied, test.want)
			}
			if after.Sequence == before.Sequence {
				t.Fatal("native journal array dropped its mutations")
			}
		})
	}
}

func TestJournalHelperFinishCorrectionInSameExecution(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered exec")
	}
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Work"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	transform.shellTurnID = "finish-turn"
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, transform.shellTurnID); err != nil {
		t.Fatal(err)
	}
	before, _, err := readThreadJournal(proxy.replayStore, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		response := httptest.NewRecorder()
		proxy.commentary.serveHTTP(response, r)
		if calls == 1 {
			after, _, readErr := readThreadJournal(proxy.replayStore, workspace, thread)
			if readErr != nil || !reflect.DeepEqual(before.Items, after.Items) || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.Receipts, after.Receipts) {
				t.Error("premature finish did not roll back")
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	proxy.commentaryEndpoint = server.URL
	lowered, changed, err := transform.lowerCodeModeCommentary("correction-call", `const rejected=await journal([{op:"log",text:"Must roll back"},{op:"finish"}]);
if (rejected.length!==0) throw new Error("finish was accepted");
const corrected=await journal([{op:"set",p:"/1",state:"done"},{op:"finish"}]);
if (corrected[0]!=="/1") throw new Error("correction failed"); text("corrected");`)
	if err != nil || !changed {
		t.Fatalf("lowering: %v %v", changed, err)
	}
	script := `const outputs=[]; globalThis.text=value=>outputs.push(value);
const tools={exec_command:async ({cmd})=>{const mutation=JSON.parse(decodeURIComponent(cmd.match(/'([^']*)'$/)[1]));
const response=await fetch(` + strconv.Quote(server.URL) + `,{method:"POST",headers:{Authorization:` + strconv.Quote("Bearer "+runtimeCommentaryToken(t, transform)) + `},body:JSON.stringify({journal:mutation,id:"publication"})});
return {exit_code:0,output:await response.text()};}};
(async()=>{` + lowered + `})().then(()=>process.stdout.write(JSON.stringify(outputs)),error=>{console.error(error);process.exitCode=1;});`
	output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("execution: %s: %v", output, err)
	}
	var outputs []string
	if err := json.Unmarshal(output, &outputs); err != nil || len(outputs) != 2 || !strings.Contains(outputs[0], "runnable task /1") || outputs[1] != "corrected" {
		t.Fatalf("correction output: %s (%v)", output, err)
	}
	server.Close()
	if calls != 2 {
		t.Fatalf("helper requests=%d want=2", calls)
	}
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := readThreadJournal(reopened, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Receipts["runtime:"+journalHostFinishReceipt(transform.shellTurnID, "correction-call")]; !ok || len(after.Items) != 1 || after.Items[0].State != "done" || after.Sequence != before.Sequence+1 || after.ResetIntent != nil || after.ResetHandledTurn != before.ResetHandledTurn {
		t.Fatal("same-turn correction lost completion or leaked continuation state")
	}
}
