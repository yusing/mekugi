package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"slices"
	"strings"
	"testing"
)

const duplicateTestHeader = "Script completed\nWall time 0.1 seconds\nOutput:\n"

func duplicateTestInput(command, id, body string) []any {
	return []any{
		continuationTestCall("exec", id, "text((await tools.exec_command({cmd:"+string(mustMarshalJSON(command))+"})).output)"),
		map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": duplicateTestHeader + body},
	}
}

func duplicateTestPrepare(t *testing.T, proxy *mekugiProxy, input []any, metadata codexTurnMetadata) *parsedResponsesRequest {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": input, "tools": testExecResponsesTools(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	metadata.RequestKind = "turn"
	metadata.Directories = map[string]jsonv1.RawMessage{t.TempDir(): nil}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	transform.Close()
	return &request
}

func duplicateTestItems(t *testing.T, request *parsedResponsesRequest) []map[string]jsonv1.RawMessage {
	t.Helper()
	var items []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDuplicateOutputPreparedMatching(t *testing.T) {
	a, b, c := strings.Repeat("alpha", 42)+"\n", strings.Repeat("bravo", 42)+"\n", strings.Repeat("charlie", 30)+"\n"
	for _, tc := range []struct{ name, command, before, after, want string }{
		{"contained", "a; b; c", a + b + c, b, "[same as `a; b; c` L2]\n"},
		{"container", "b", b, a + b + c, a + "[same as `b`]\n" + c},
		{"changed batch", "a; b; c", a + b + c, "CCC2\n" + b, "CCC2\n[same as `a; b; c` L2]\n"},
		{"diff headers", "mchanges amber1", "file.go\n@@ old @@\n" + a + b, "diff --git a/file.go b/file.go\nindex abc..def\n@@ new @@\n" + a + b, "diff --git a/file.go b/file.go\nindex abc..def\n@@ new @@\n[same as `mchanges amber1` L3-4]\n"},
		{"small edit", "cat f.go", a + b + c, a + "changed region\n" + c, "[same as `cat f.go` L1]\nchanged region\n[same as `cat f.go` L3]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.duplicateOutput = true
			input := append(duplicateTestInput(tc.command, "a", tc.before), duplicateTestInput("new command", "b", tc.after)...)
			request := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
			items := duplicateTestItems(t, request)
			if got := jsonString(items[3], "output"); got != duplicateTestHeader+tc.want {
				t.Fatalf("output = %q, want %q", got, duplicateTestHeader+tc.want)
			}
			if got := jsonString(items[1], "output"); got != duplicateTestHeader+tc.before {
				t.Fatal("source output changed")
			}
			if !sameJSONValue(request.originalFields["input"], mustMarshalJSON(input)) {
				t.Fatal("original host input changed")
			}
			projected := bytes.Clone(request.fields["input"])
			fresh := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
			if !bytes.Equal(projected, fresh.fields["input"]) {
				t.Fatal("same original input projected differently")
			}
		})
	}
}

func TestDuplicateOutputHostPartsAndExclusions(t *testing.T) {
	body := strings.Repeat("unique host output ", 20) + "\n"
	warning := "router-only notice\n" + body
	session := string(mustMarshalJSON(map[string]any{"output": body, "session_id": 42}))
	input := duplicateTestInput("cat file", "source", body)
	input = append(input,
		continuationTestOutput("parts", duplicateTestHeader, body),
		map[string]any{"type": "function_call_output", "call_id": "native", "output": "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 1\nFinal output:\n" + body},
		continuationTestOutput("session", session),
		continuationTestOutput("session2", session),
		continuationTestOutput("", body),
		map[string]any{"type": "function_call_output", "call_id": "image", "output": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,example"}}},
	)
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(input)}}
	counts := duplicateOutputHostParts(request.fields["input"])
	items := duplicateTestItems(t, request)
	// Use the production append owner after the original boundary was recorded.
	for _, at := range []int{1, 2, 3} {
		appended, _, err := appendToolOutputWarning(items[at]["output"], warning)
		if err != nil {
			t.Fatal(err)
		}
		items[at]["output"] = appended
	}
	request.setInput(mustMarshalJSON(items))
	projectDuplicateOutputs(request, counts, "exec")
	got := duplicateTestItems(t, request)
	for _, at := range []int{1, 2, 3} {
		texts := executionOutputTexts(got[at]["output"])
		if texts[len(texts)-1] != warning {
			t.Fatal("router append changed")
		}
	}
	if texts := executionOutputTexts(got[2]["output"]); texts[0] != duplicateTestHeader || texts[1] != "[same as `cat file`]\n" {
		t.Fatalf("multipart output = %q", texts)
	}
	if text := executionOutputTexts(got[3]["output"])[0]; text != "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 1\nFinal output:\n[same as `cat file`]\n" {
		t.Fatalf("native header changed: %q", text)
	}
	for at := 4; at < len(got); at++ {
		if !sameJSONValue(mustMarshalJSON(got[at]), mustMarshalJSON(items[at])) {
			t.Fatalf("excluded item %d changed", at)
		}
	}
}

func TestDuplicateOutputEvidenceConsumersReadOriginal(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	proxy.duplicateOutput = true
	failure := "Script error:\n" + strings.Repeat("full failure detail\n", 20)
	input := duplicateTestInput("previous", "source", failure)
	input = append(input, continuationTestCall("exec", "failed", "broken()"), continuationTestOutput("failed", "Script failed\nWall time 0.1 seconds\nOutput:\n", failure))
	request := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
	items := duplicateTestItems(t, request)
	if !strings.Contains(string(items[3]["output"]), "same as") {
		t.Fatal("failure body was not projected")
	}
	proxy.activity.mu.Lock()
	defer proxy.activity.mu.Unlock()
	found := false
	for _, event := range proxy.activity.events {
		if event.callID == "failed" {
			found = true
			if event.errorDetail != "exec script failed: "+strings.TrimPrefix(failure, "Script error:\n") {
				t.Fatalf("failure observer saw projected content: %q", event.errorDetail)
			}
		}
	}
	if !found {
		t.Fatal("failure observer missed original result")
	}
}

func TestDuplicateOutputPreparedContinuationAndScope(t *testing.T) {
	body := strings.Repeat("visible command text ", 20) + "\n"
	for _, scope := range []string{"ordinary", "compacted", "fork", "disabled"} {
		t.Run(scope, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.duplicateOutput = scope != "disabled"
			input := duplicateTestInput("earlier", "a", body)
			metadata := codexTurnMetadata{}
			if scope == "compacted" {
				input = nil
			}
			if scope == "fork" {
				metadata.SubagentKind, metadata.AgentName, metadata.ParentThreadID = "thread_spawn", "/root/child", "parent"
			}
			input = append(input, continuationTestCall("exec", "running", "text('yield')"), continuationTestOutput("running", "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n"+body))
			request := duplicateTestPrepare(t, proxy, input, metadata)
			items := duplicateTestItems(t, request)
			texts := executionOutputTexts(items[len(items)-1]["output"])
			if len(texts) != 2 || !strings.Contains(texts[1], `"cell_id":"live-42"`) {
				t.Fatalf("continuation advice lost: %q", texts)
			}
			want := "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n" + body
			if scope == "ordinary" || scope == "fork" {
				want = "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n[same as `earlier`]\n"
			}
			if texts[0] != want {
				t.Fatalf("scoped output = %q", texts[0])
			}
		})
	}
}

func TestDuplicateOutputProviderPrefix(t *testing.T) {
	body := strings.Repeat("prefix command output ", 20) + "\n"
	input := append(duplicateTestInput("cat file", "a", body), duplicateTestInput("cat file", "b", body)...)
	project := func(input []any) *parsedResponsesRequest {
		proxy := newManagedMekugiProxy(t)
		proxy.duplicateOutput = true
		return duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
	}
	first := project(input)
	var original []jsonv1.RawMessage
	if err := json.Unmarshal(first.fields["input"], &original); err != nil {
		t.Fatal(err)
	}
	state, err := (providerHistory{confirmed: true}).append(original)
	if err != nil {
		t.Fatal(err)
	}
	grownInput := append(slices.Clone(input), duplicateTestInput("cat again", "c", body)...)
	// A later occurrence with a different part count must not change the prefix.
	grown := project(append(grownInput, continuationTestOutput("b", duplicateTestHeader, body)))
	items := duplicateTestItems(t, grown)
	if got := executionOutputTexts(items[len(items)-1]["output"]); len(got) != 2 || got[0] != duplicateTestHeader || got[1] != "[same as `cat file`]\n" {
		t.Fatalf("repeated output identity used the wrong part boundary: %q", got)
	}
	exchange := &webSocketExchange{parentID: "parent", history: &webSocketHistory{parent: &webSocketHistory{providerHistory: state}}}
	grown.fields["previous_response_id"] = mustMarshalJSON("parent")
	if err := exchange.reconcileProviderHistory(grown, mustMarshalJSON(grown.fields)); err != nil {
		t.Fatal(err)
	}
	if grown.cachedInput != len(original) || grown.rebaseInput {
		t.Fatal("growing projection rebased a confirmed prefix")
	}
}

func TestDuplicateOutputLabels(t *testing.T) {
	input := duplicateTestInput("first", "a", strings.Repeat("output text\n", 25))
	call := input[0].(map[string]jsonv1.RawMessage)
	call["input"] = mustMarshalJSON(`text((await tools.exec_command({cmd:"first"})).output); text((await tools.exec_command({cmd:"second"})).output)`)
	if command := duplicateOutputCommand(call, "exec"); command != "" {
		t.Fatal("batch acquired a single-command label")
	}
	if label := duplicateOutputLabel("", "batch: exit_code=0\nrest"); label != "batch: exit_code=0" {
		t.Fatal(label)
	}
	if label := duplicateOutputLabel("cat `file`\n"+strings.Repeat("long", 30), ""); len([]rune(label)) != 60 || strings.ContainsAny(label, "`\n") {
		t.Fatal(label)
	}
}

func TestDuplicateOutputMarkersPointToVerbatim(t *testing.T) {
	var input []any
	var combined string
	for _, word := range []string{"alpha", "bravo", "charlie"} {
		body := strings.Repeat(word, 45) + "\n"
		combined += body
		input = append(input, duplicateTestInput(word+strings.Repeat(" command", 10), word, body)...)
	}
	input = append(input, duplicateTestInput("combined", "d", combined)...)
	input = append(input, duplicateTestInput("combined again", "e", combined)...)
	literal := "[same as `" + strings.Repeat("literal host content ", 20) + "`]\n"
	input = append(input, duplicateTestInput("literal", "f", literal)...)
	input = append(input, duplicateTestInput("literal again", "g", literal)...)
	original := mustMarshalJSON(input)
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": original}}
	projectDuplicateOutputs(request, duplicateOutputHostParts(request.fields["input"]), "exec")
	first := bytes.Clone(request.fields["input"])
	items := duplicateTestItems(t, request)
	if jsonString(items[7], "output") != jsonString(items[9], "output") || strings.Contains(jsonString(items[9], "output"), "combined") {
		t.Fatal("new markers referenced a marker-only unit")
	}
	if jsonString(items[11], "output") != duplicateTestHeader+literal || jsonString(items[13], "output") != duplicateTestHeader+"[same as `literal`]\n" {
		t.Fatal("literal host marker text did not remain a verbatim source")
	}
	request.setInput(original)
	projectDuplicateOutputs(request, duplicateOutputHostParts(original), "exec")
	if !bytes.Equal(first, request.fields["input"]) {
		t.Fatal("same original input projected differently")
	}
}
