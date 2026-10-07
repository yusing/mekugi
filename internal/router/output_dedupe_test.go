package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"net/http"
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
		{"contained", "a; b; c", a + b + c, b, "[same as O1 L2]\n"},
		{"container", "b", b, a + b + c, "[O2]\n" + a + "[same as O1]\n" + c},
		{"changed batch", "a; b; c", a + b + c, "CCC2\n" + b, "[O2]\nCCC2\n[same as O1 L2]\n"},
		{"diff headers", "mchanges amber1", "file.go\n@@ old @@\n" + a + b, "diff --git a/file.go b/file.go\nindex abc..def\n@@ new @@\n" + a + b, "[O2]\ndiff --git a/file.go b/file.go\nindex abc..def\n@@ new @@\n[same as O1 L3-4]\n"},
		{"small edit", "cat f.go", a + b + c, a + "changed region\n" + c, "[O2]\n[same as O1 L1]\nchanged region\n[same as O1 L3]\n"},
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
			if got := jsonString(items[1], "output"); got != duplicateTestHeader+"[O1]\n"+tc.before {
				t.Fatal("source anchor or verbatim body changed")
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
	projectDuplicateOutputs(request, counts)
	got := duplicateTestItems(t, request)
	for _, at := range []int{1, 2, 3} {
		texts := executionOutputTexts(got[at]["output"])
		if texts[len(texts)-1] != warning {
			t.Fatal("router append changed")
		}
	}
	if texts := executionOutputTexts(got[2]["output"]); texts[0] != duplicateTestHeader || texts[1] != "[same as O1]\n" {
		t.Fatalf("multipart output = %q", texts)
	}
	if text := executionOutputTexts(got[3]["output"])[0]; text != "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 1\nFinal output:\n[same as O1]\n" {
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
			if scope == "compacted" {
				want = "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n[O1]\n" + body
			}
			if scope == "ordinary" || scope == "fork" {
				want = "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n[same as O1]\n"
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
	// The source must already have its anchor before the first repeat arrives.
	first := project(input[:2])
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
	if got := executionOutputTexts(items[len(items)-1]["output"]); len(got) != 2 || got[0] != duplicateTestHeader || got[1] != "[same as O1]\n" {
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

func TestDuplicateOutputSourceIDs(t *testing.T) {
	a := "package router\n" + strings.Repeat("alpha source body\n", 20)
	b := "package router\n" + strings.Repeat("bravo source body\n", 20)
	c := "package router\n" + strings.Repeat("charlie source body\n", 20)
	frame := frameComposerFile("/missing/source.go", a)[0]
	header, _, _ := strings.Cut(frame, "\n")
	input := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{frame})}}},
		continuationTestOutput("batch", duplicateTestHeader, b, c),
		continuationTestOutput("repeat", a, b, c),
	}
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(input)}}
	projectDuplicateOutputs(request, duplicateOutputHostParts(request.fields["input"]))
	items := duplicateTestItems(t, request)
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(items[0]["content"], &parts); err != nil {
		t.Fatal(err)
	}
	frames, ok := decodeFileAttachments(parts[0].Text)
	if !ok || len(frames) != 1 || frames[0] != header+"\n[O1]\n"+a {
		t.Fatalf("attachment source = %q", frames)
	}
	if got, want := executionOutputTexts(items[1]["output"]), []string{duplicateTestHeader, "[O2]\n" + b, "[O3]\n" + c}; !slices.Equal(got, want) {
		t.Fatalf("sources with the same first line = %q, want %q", got, want)
	}
	if got, want := executionOutputTexts(items[2]["output"]), []string{"[same as O1]\n", "[same as O2]\n", "[same as O3]\n"}; !slices.Equal(got, want) {
		t.Fatalf("multipart references = %q, want %q", got, want)
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
	projectDuplicateOutputs(request, duplicateOutputHostParts(request.fields["input"]))
	first := bytes.Clone(request.fields["input"])
	items := duplicateTestItems(t, request)
	if jsonString(items[7], "output") != jsonString(items[9], "output") || strings.Contains(jsonString(items[9], "output"), "combined") {
		t.Fatal("new markers referenced a marker-only unit")
	}
	if jsonString(items[11], "output") != duplicateTestHeader+"[O4]\n"+literal || jsonString(items[13], "output") != duplicateTestHeader+"[same as O4]\n" {
		t.Fatal("literal host marker text did not remain a verbatim source")
	}
	request.setInput(original)
	projectDuplicateOutputs(request, duplicateOutputHostParts(original))
	if !bytes.Equal(first, request.fields["input"]) {
		t.Fatal("same original input projected differently")
	}
}

func TestDuplicateOutputAttachmentsAndWirePrefix(t *testing.T) {
	var body string
	for i := range 12 {
		body += fmt.Sprintf("unchanged submitted content %02d\n", i)
	}
	user := func(text string) any {
		return map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
	}
	for _, tc := range []struct {
		name  string
		frame func(string) string
	}{
		{"file snapshot", func(body string) string { return frameComposerFile("/missing/file.go", body)[0] }},
		{"skill snapshot", func(body string) string { return frameComposerSkillFromPath("review", "/missing/SKILL.md", body)[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.frame(body)
			header, _, ok := strings.Cut(frame, "\n")
			header += "\n"
			if !ok {
				t.Fatal("producer frame was not eligible")
			}
			attach := func(text string) any {
				return user(encodeFileAttachments([]string{text}))
			}
			input := []any{attach(frame), user(body), attach(frame)}
			for _, enabled := range []bool{false, true} {
				proxy := newManagedMekugiProxy(t)
				proxy.duplicateOutput = enabled
				wire := func(input []any) *parsedResponsesRequest {
					request := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
					if fresh := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{}); !bytes.Equal(request.fields["input"], fresh.fields["input"]) {
						t.Fatal("same original attachment input projected differently")
					}
					attempt := newRequestAttempt(requestExecutor{provider: &serverFakeProvider{}, mekugiCalls: proxy}, t.Context(), t.Context(), *request, http.Header{}, "")
					if err := attempt.prepareWire(); err != nil {
						t.Fatal(err)
					}
					return &attempt.request
				}
				request := wire(input)
				items := duplicateTestItems(t, request)
				texts := func(item map[string]jsonv1.RawMessage) string {
					var parts []struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(item["content"], &parts); err != nil {
						t.Fatal(err)
					}
					return parts[0].Text
				}
				source, want := frame, frame
				if enabled {
					source = header + "[O1]\n" + body
					want = header + "[same as O1]\n"
				}
				if texts(items[0]) != source || texts(items[1]) != body || texts(items[2]) != want {
					t.Fatalf("wire content changed incorrectly: %s", request.fields["input"])
				}
				if !sameJSONValue(request.originalFields["input"], mustMarshalJSON(input)) {
					t.Fatal("original snapshot changed")
				}
				if !enabled || tc.name != "skill snapshot" {
					continue
				}
				first := request.fields["input"]
				var prefix []jsonv1.RawMessage
				if err := json.Unmarshal(first, &prefix); err != nil {
					t.Fatal(err)
				}
				state, err := (providerHistory{confirmed: true}).append(prefix)
				if err != nil {
					t.Fatal(err)
				}
				grown := wire(append(slices.Clone(input), attach(frame)))
				if texts(duplicateTestItems(t, grown)[3]) != want {
					t.Fatal("third snapshot referenced a generated marker")
				}
				exchange := &webSocketExchange{parentID: "parent", history: &webSocketHistory{parent: &webSocketHistory{providerHistory: state}}}
				grown.fields["previous_response_id"] = mustMarshalJSON("parent")
				if err := exchange.reconcileProviderHistory(grown, mustMarshalJSON(grown.fields)); err != nil {
					t.Fatal(err)
				}
				if grown.cachedInput != len(prefix) || grown.rebaseInput {
					t.Fatal("attachment projection rewrote the confirmed prefix")
				}
			}
		})
	}
}

func TestDuplicateOutputAttachmentEligibility(t *testing.T) {
	var body string
	for i := range 12 {
		body += fmt.Sprintf("attached body source text %02d\n", i)
	}
	frame := frameComposerFile("/missing/example", body)[0]
	user := func(content any) any { return map[string]any{"role": "user", "content": content} }
	parts := []any{map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{frame})}, map[string]any{"type": "input_image", "image_url": "image"}}
	classified := map[string]any{"role": "user", "content": parts, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"agents_md.instructions", "user.image"}}}
	input := []any{
		classified,
		map[string]any{"role": "developer", "content": parts},
		user(fileAttachmentPrefix + "invalid JSON" + fileAttachmentSuffix),
		user("Attached file /missing: CONTENT NOT ATTACHED\n" + body),
		user(body),
		user([]any{map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{frame})}}),
	}
	input = append(input, duplicateTestInput("read file", "output", body)...)
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(input)}}
	projectDuplicateOutputs(request, duplicateOutputHostParts(request.fields["input"]))
	items := duplicateTestItems(t, request)
	for at := range 5 {
		if !sameJSONValue(mustMarshalJSON(items[at]), mustMarshalJSON(input[at])) {
			t.Fatalf("ineligible item %d changed", at)
		}
	}
	header, _, _ := strings.Cut(frame, "\n")
	header += "\n"
	source := user([]any{map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{header + "[O1]\n" + body})}})
	if !sameJSONValue(mustMarshalJSON(items[5]), mustMarshalJSON(source)) {
		t.Fatal("eligible attachment source lost its anchor or verbatim body")
	}
	if got := jsonString(items[7], "output"); got != duplicateTestHeader+"[same as O1]\n" {
		t.Fatalf("tool did not reference the visible attachment: %q", got)
	}
}

func TestDuplicateOutputAttachmentDeliveredShape(t *testing.T) {
	var body string
	for i := range 12 {
		body += fmt.Sprintf("distinct delivered frame row %02d\n", i)
	}
	frame := frameComposerFile("/missing/delivery", body)[0]
	envelope := encodeFileAttachments([]string{frame})
	part := func(kind, text string) any { return map[string]any{"type": kind, "text": text} }
	for _, content := range []any{
		envelope,
		[]any{part("text", envelope)},
		[]any{part("input_text", envelope), part("input_text", frame)},
		[]any{part("input_text", frame), part("input_text", envelope)},
	} {
		input := []any{map[string]any{"role": "user", "content": content}}
		input = append(input, duplicateTestInput("later body", "later", body)...)
		proxy := newManagedMekugiProxy(t)
		proxy.duplicateOutput = true
		request := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
		attempt := newRequestAttempt(requestExecutor{provider: &serverFakeProvider{}, mekugiCalls: proxy}, t.Context(), t.Context(), *request, http.Header{}, "")
		if err := attempt.prepareWire(); err != nil {
			t.Fatal(err)
		}
		items := duplicateTestItems(t, &attempt.request)
		// Unsupported shapes must not become invisible sources. Supported
		// envelopes move after ordinary parts, which must stay full and earlier.
		_, array := content.([]any)
		supported := array && len(content.([]any)) == 2
		if strings.Contains(jsonString(items[len(items)-1], "output"), "[same as") != supported {
			t.Fatalf("wrong source eligibility for %T: %s", content, attempt.request.fields["input"])
		}
		if supported {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(items[0]["content"], &parts); err != nil {
				t.Fatal(err)
			}
			if parts[0].Text != frame {
				t.Fatal("first delivered frame references later text")
			}
		}
	}
}
