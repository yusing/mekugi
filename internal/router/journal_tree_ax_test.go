package router

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestJournalLogResolvesUnderSpecifiedTargets(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Parser","state":"working","tasks":[{"title":"Tokenizer","state":"working"},{"title":"AST","state":"working"}]}`),
		jsontext.Value(`{"title":"Renderer","state":"working"}`),
	}})
	for _, test := range []struct {
		name  string
		setup []journalMutation
		p     string
		want  string
	}{
		// /1/1, /1/2 and /2 are working leaves without a common task.
		{name: "unrelated working leaves", want: "/3"},
		{name: "sibling working leaves", setup: []journalMutation{{Op: "set", P: "/2", State: new("done")}}, want: "/1/3"},
		{name: "note path", p: "/1/3", want: "/1/4"},
		{name: "root note path", p: "/3", want: "/4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.setup) != 0 {
				treeApply(t, proxy, workspace, test.setup...)
			}
			got := treeApply(t, proxy, workspace, journalMutation{Op: "log", P: test.p, Text: new("Established fact")})
			if !reflect.DeepEqual(got, []string{test.want}) {
				t.Fatalf("log path = %v, want %s", got, test.want)
			}
		})
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "log", P: "/9", Text: new("Lost")}}); err == nil || !strings.Contains(err.Error(), "journal path not found: /9") {
		t.Fatalf("missing log path error = %v", err)
	}
}

func TestJournalRejectionsNameTheCorrection(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace,
		journalMutation{Op: "add", Kind: "task", Title: new("Parser")},
		journalMutation{Op: "add", Under: "/1", Title: new("Tokenizer is table driven")},
		journalMutation{Op: "add", Title: new("Root finding")},
	)
	for _, test := range []struct {
		name      string
		mutations []journalMutation
		want      string
	}{
		{name: "nested note parent", mutations: []journalMutation{{Op: "add", Under: "/1/1", Title: new("Detail")}},
			want: `journal parent must be a task: /1/1 is a note ("Tokenizer is table driven"); use its task /1 or omit under for the root`},
		{name: "root note parent", mutations: []journalMutation{{Op: "plan", Under: "/2", Tasks: []jsontext.Value{jsontext.Value(`"Step"`)}}},
			want: `journal parent must be a task: /2 is a note ("Root finding"); omit under for the root`},
		{name: "batch position", mutations: []journalMutation{{Op: "log", Text: new("Fine")}, {Op: "set", P: "/7", State: new("done")}},
			want: "operation 2 (set): journal path not found: /7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", test.mutations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
	if journal := treeSnapshot(t, proxy, workspace); len(journal.Items) != 3 {
		t.Fatalf("rejected batch applied: %+v", journal.Items)
	}
}

// A rejected mutation is the model's correction, not a transport failure, so it
// must not abort the rest of the Code Mode program.
func TestCodeModeJournalRejectionDoesNotAbortProgram(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered Code Mode")
	}
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Title: new("Root finding")})
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	token := proxy.commentary.subscribe(workspace+"\x00session", "reject-call")
	proxy.commentary.bindActivity(token, "tree")
	publish := func(body string) string {
		t.Helper()
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(body)})
		if !matched || err != nil {
			t.Fatalf("publication %s: matched=%v err=%v", body, matched, err)
		}
		return output.String()
	}
	var rejected struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(publish(`{"op":"add","under":"/1","title":"Detail"}`)), &rejected); err != nil || rejected.OK || !strings.HasPrefix(rejected.Error, "journal mutation rejected: journal parent must be a task: /1 is a note") {
		t.Fatalf("rejection = %+v, %v", rejected, err)
	}
	if err := json.Unmarshal([]byte(publish(`[{"op":"log","path":"/1","text":"Typo"}]`)), &rejected); err != nil || rejected.OK || !strings.Contains(rejected.Error, `unknown field "path"`) {
		t.Fatalf("decode rejection = %+v, %v", rejected, err)
	}

	transform, _ := newRuntimeCommentaryTransform(t)
	lowered, changed, err := transform.lowerCodeModeCommentary("reject-call", `const single = await journal({op:"add",under:"/1",title:"Detail"});
const batch = await journal([{op:"log",text:"Fine"},{op:"set",p:"/9",state:"done"}]);
text("continued " + JSON.stringify([single, batch]));`)
	if err != nil || !changed {
		t.Fatalf("lowering: changed=%t err=%v", changed, err)
	}
	script := `const outputs=[]; globalThis.text=value=>outputs.push(value);
const tools={exec_command:async ()=>({exit_code:0,output:` + strconv.Quote(`{"ok":false,"error":"journal mutation rejected: example"}`) + `})};
(async()=>{` + lowered + `})().then(()=>process.stdout.write(JSON.stringify(outputs)),error=>{console.error(error);process.exitCode=1;});`
	output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("lowered execution: %s: %v", output, err)
	}
	var outputs []string
	if err := json.Unmarshal(output, &outputs); err != nil {
		t.Fatalf("outputs %s: %v", output, err)
	}
	want := []string{"journal mutation rejected: example", "journal mutation rejected: example", "continued [null,[]]"}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("outputs = %q, want %q", outputs, want)
	}
}
