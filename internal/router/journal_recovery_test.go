package router

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestJournalTaskRecoveryAfterOutcomeAndRestart(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Completed one","state":"done"}`),
		jsontext.Value(`{"title":"Completed two","state":"done"}`),
		jsontext.Value(`{"title":"Completed three","state":"done"}`),
	}})
	// The router-owned answer takes the next shared ordinal, not a task ID.
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Text: new("Previous turn outcome"), Answer: new(true)})
	paths := treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Next one","body":"Long task evidence","tasks":["Nested"]}`),
		jsontext.Value(`"Next two"`), jsontext.Value(`"Next three"`),
	}})
	if !reflect.DeepEqual(paths, []string{"/5", "/5/1", "/6", "/7"}) {
		t.Fatalf("plan lost actual path mapping: %v", paths)
	}
	before := treeSnapshot(t, proxy, workspace)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{
		{Op: "set", P: "/5", State: new("working")}, {Op: "set", P: "/4", State: new("working")},
	}); err == nil {
		t.Fatal("guessed outcome ID was writable")
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected batch changed the journal")
	}
	for _, child := range []string{"child", "sibling"} {
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, child, "", []journalMutation{{Op: "log", Text: new(strings.Repeat("Unrelated child result ", 100))}}); err != nil {
			t.Fatal(err)
		}
	}
	proxy.journals = newJournalStore()
	for _, test := range []struct {
		view          string
		depth         *int
		roots, nested int
	}{
		{"tasks", new(0), 6, 0}, {"tasks", new(2), 6, 1}, {"own", new(0), 7, 0},
	} {
		nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", test.depth, test.view)
		if err != nil || len(nodes) != test.roots {
			t.Fatalf("%s read: %v, %v", test.view, nodes, err)
		}
		if test.view == "tasks" {
			for _, node := range nodes {
				if node.Kind != "task" || node.Body != "" || node.Question != "" || strings.Contains(node.Path, "@") {
					t.Fatalf("noncompact recovery node: %+v", node)
				}
			}
			if len(nodes[3].Children) != test.nested || nodes[3].Path != paths[0] || nodes[4].Path != paths[2] || nodes[5].Path != paths[3] {
				t.Fatalf("recovery paths/depth: %+v", nodes)
			}
		}
	}
	if _, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", ""), "/@agents/@child/1"); !ok {
		t.Fatal("default read no longer mounts agents")
	}
	for _, view := range []string{"tasks", "own"} {
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "child", "/root", "", new(0), view); err != nil {
			t.Fatalf("%s ancestor read rejected: %v", view, err)
		}
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "child", "/root/sibling", "", new(0), view); err == nil {
			t.Fatalf("%s read bypassed ancestry authorization", view)
		}
	}
	// Local recovery must not even need a readable child record.
	child, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil {
		t.Fatal(err)
	}
	child.Receipts = nil // Keep identity provable while content validation fails.
	corrupt, err := json.Marshal(child, json.FormatNilMapAsNull(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "child")), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"own", "tasks"} {
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", new(0), view); err != nil {
			t.Fatalf("%s recovery depends on unreadable descendant: %v", view, err)
		}
	}
	if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", new(0), "combined"); err == nil {
		t.Fatal("combined read silently lost corrupt mounted evidence")
	}
}

func TestJournalCompactReadAuthenticatedTransports(t *testing.T) {
	t.Parallel()
	transform, proxy, _, _ := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
		{Op: "add", Title: new("Not a task")},
		{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`{"title":"Task","body":"Not needed for IDs"}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	token := proxy.commentary.subscribe(transform.historySessionID, "recovery")
	proxy.commentary.bindActivity(token, transform.shellThreadID)
	for _, view := range []string{"tasks", "own", "unknown"} {
		arguments := `{"op":"read","view":"` + view + `","depth":0}`
		result, err := transform.executeJournalCall(map[string]jsonv1.RawMessage{
			"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
			"call_id": mustMarshalJSON("recovery-" + view), "arguments": mustMarshalJSON(arguments),
		})
		if err != nil {
			t.Fatal(err)
		}
		var native struct {
			OK    bool          `json:"ok"`
			Items []journalNode `json:"items"`
		}
		if err := json.Unmarshal([]byte(jsonString(result, "output")), &native); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		_, carrierErr := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(arguments)})
		if view == "unknown" {
			if native.OK || carrierErr == nil {
				t.Fatal("invalid view accepted")
			}
			continue
		}
		if !native.OK || carrierErr != nil {
			t.Fatalf("%s read: native=%v carrier=%v", view, native.OK, carrierErr)
		}
		var carrier struct {
			Items []journalNode `json:"items"`
		}
		if err := json.Unmarshal(output.Bytes(), &carrier); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(carrier.Items, native.Items) {
			t.Fatalf("%s transport mismatch: %s", view, output.String())
		}
		if view == "tasks" && (len(native.Items) != 1 || native.Items[0].Path != "/2" || native.Items[0].Body != "") {
			t.Fatalf("task read: %+v", native.Items)
		}
	}
}

func TestCodeModeJournalPlanPathsAreVisibleAndReturned(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered Code Mode")
	}
	for _, mutation := range []string{`{op:"plan",tasks:["One","Two","Three"]}`, `[{op:"plan",tasks:["One","Two","Three"]}]`} {
		transform, _ := newRuntimeCommentaryTransform(t)
		lowered, changed, err := transform.lowerCodeModeCommentary("plan-call", `const paths = await journal(`+mutation+`); text({returned:paths});`)
		if err != nil || !changed {
			t.Fatalf("lowering: %v %v", changed, err)
		}
		script := `const outputs=[]; globalThis.text=value=>outputs.push(value); let calls=0;
const tools={exec_command:async ()=>{if(++calls>1) throw new Error("unexpected extra read"); return {exit_code:0,output:` + strconv.Quote(`{"ok":true,"items":["/5","/6","/7"]}`) + `};}};
(async()=>{` + lowered + `})().then(()=>process.stdout.write(JSON.stringify(outputs)),error=>{console.error(error);process.exitCode=1;});`
		output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
		if err != nil {
			t.Fatalf("execution: %s: %v", output, err)
		}
		want := `["journal paths: [\"/5\",\"/6\",\"/7\"]",{"returned":["/5","/6","/7"]}]`
		if string(output) != want {
			t.Fatalf("paths not visible/returned: %s", output)
		}
	}
}
