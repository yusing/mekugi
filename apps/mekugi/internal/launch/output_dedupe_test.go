package launch

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/modules/conversation/composer"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/outputdeduplication"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func testInputProjections() []binding.InputProjection {
	registry := new(binding.Registry)
	service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
	if err != nil {
		panic(err)
	}
	if err := outputdeduplication.Module.Bindings.Register(registry, service, nil); err != nil {
		panic(err)
	}
	return registry.InputProjections()
}

const duplicateTestHeader = "Script completed\nWall time 0.1 seconds\nOutput:\n"

func duplicateTestInput(command, id, body string) []any {
	return []any{
		continuationTestCall("exec", id, "text((await tools.exec_command({cmd:"+string(responses.MustMarshalJSON(command))+"})).output)"),
		map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": duplicateTestHeader + body},
	}
}

func duplicateTestPrepare(t *testing.T, proxy *observationFixture, input []any, metadata codexresponses.TurnMetadata) *codexresponses.Request {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
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

func duplicateTestItems(t *testing.T, request *codexresponses.Request) []map[string]jsonv1.RawMessage {
	t.Helper()
	var items []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
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
			proxy.inputProjections = testInputProjections()
			input := append(duplicateTestInput(tc.command, "a", tc.before), duplicateTestInput("new command", "b", tc.after)...)
			request := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
			items := duplicateTestItems(t, request)
			if got := responses.StringField(items[3], "output"); got != duplicateTestHeader+tc.want {
				t.Fatalf("output = %q, want %q", got, duplicateTestHeader+tc.want)
			}
			if got := responses.StringField(items[1], "output"); got != duplicateTestHeader+"[O1]\n"+tc.before {
				t.Fatal("source anchor or verbatim body changed")
			}
			if !responses.SameJSONValue(request.OriginalField("input"), responses.MustMarshalJSON(input)) {
				t.Fatal("original host input changed")
			}
			projected := bytes.Clone(request.Fields["input"])
			fresh := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
			if !bytes.Equal(projected, fresh.Fields["input"]) {
				t.Fatal("same original input projected differently")
			}
		})
	}
}

func TestDuplicateOutputHostPartsAndExclusions(t *testing.T) {
	body := strings.Repeat("unique host output ", 20) + "\n"
	warning := "router-only notice\n" + body
	session := string(responses.MustMarshalJSON(map[string]any{"output": body, "session_id": 42}))
	input := duplicateTestInput("cat file", "source", body)
	input = append(input,
		continuationTestOutput("parts", duplicateTestHeader, body),
		map[string]any{"type": "function_call_output", "call_id": "native", "output": "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 1\nFinal output:\n" + body},
		continuationTestOutput("session", session),
		continuationTestOutput("session2", session),
		continuationTestOutput("", body),
		map[string]any{"type": "function_call_output", "call_id": "image", "output": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,example"}}},
	)
	request := &codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON(input)}}
	project := testInputProjections()[0].Prepare(request.Fields["input"])
	items := duplicateTestItems(t, request)
	// Use the production append owner after the original boundary was recorded.
	for _, at := range []int{1, 2, 3} {
		appended, _, err := codexresponses.AppendOutputNotice(items[at]["output"], warning)
		if err != nil {
			t.Fatal(err)
		}
		items[at]["output"] = appended
	}
	request.Fields["input"] = responses.MustMarshalJSON(items)
	request.Fields["input"] = project(request.Fields["input"]).Input
	got := duplicateTestItems(t, request)
	for _, at := range []int{1, 2, 3} {
		texts := binding.ExecutionOutputTexts(got[at]["output"])
		if texts[len(texts)-1] != warning {
			t.Fatal("router append changed")
		}
	}
	if texts := binding.ExecutionOutputTexts(got[2]["output"]); texts[0] != duplicateTestHeader || texts[1] != "[same as O1]\n" {
		t.Fatalf("multipart output = %q", texts)
	}
	if text := binding.ExecutionOutputTexts(got[3]["output"])[0]; text != "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 1\nFinal output:\n[same as O1]\n" {
		t.Fatalf("native header changed: %q", text)
	}
	for at := 4; at < len(got); at++ {
		if !responses.SameJSONValue(responses.MustMarshalJSON(got[at]), responses.MustMarshalJSON(items[at])) {
			t.Fatalf("excluded item %d changed", at)
		}
	}
}

func TestDuplicateOutputEvidenceConsumersReadOriginal(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	proxy.inputProjections = testInputProjections()
	failure := "Script error:\n" + strings.Repeat("full failure detail\n", 20)
	input := duplicateTestInput("previous", "source", failure)
	input = append(input, continuationTestCall("exec", "failed", "broken()"), continuationTestOutput("failed", "Script failed\nWall time 0.1 seconds\nOutput:\n", failure))
	request := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
	items := duplicateTestItems(t, request)
	if !strings.Contains(string(items[3]["output"]), "same as") {
		t.Fatal("failure body was not projected")
	}
	proxy.activity.Attach("thread")
	found := false
	for _, event := range takeNativeActivity(proxy.activity, "thread") {
		if event.CallID == "failed" && event.Kind == "error" {
			found = true
			if event.ErrorDetail != "exec script failed: "+strings.TrimPrefix(failure, "Script error:\n") {
				t.Fatalf("failure observer saw projected content: %q", event.ErrorDetail)
			}
		}
	}
	if !found {
		t.Fatal("failure observer missed original result")
	}
}

func TestDuplicateOutputPreservesHostContinuationAcrossScopes(t *testing.T) {
	body := strings.Repeat("visible command text ", 20) + "\n"
	for _, scope := range []string{"ordinary", "compacted", "fork", "disabled"} {
		t.Run(scope, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			if scope != "disabled" {
				proxy.inputProjections = testInputProjections()
			}
			input := duplicateTestInput("earlier", "a", body)
			metadata := codexresponses.TurnMetadata{}
			if scope == "compacted" {
				input = nil
			}
			if scope == "fork" {
				metadata.SubagentKind, metadata.AgentName, metadata.ParentThreadID = "thread_spawn", "/root/child", "parent"
			}
			input = append(input, continuationTestCall("exec", "running", "text('yield')"), continuationTestOutput("running", "Script running with cell ID live-42\nWall time 0.1 seconds\nOutput:\n"+body))
			request := duplicateTestPrepare(t, proxy, input, metadata)
			items := duplicateTestItems(t, request)
			texts := binding.ExecutionOutputTexts(items[len(items)-1]["output"])
			if len(texts) != 1 {
				t.Fatalf("host output parts changed: %q", texts)
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
	project := func(input []any) *codexresponses.Request {
		proxy := newManagedMekugiProxy(t)
		proxy.inputProjections = testInputProjections()
		return duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
	}
	// The source must already have its anchor before the first repeat arrives.
	first := project(input[:2])
	var original []jsonv1.RawMessage
	if err := json.Unmarshal(first.Fields["input"], &original); err != nil {
		t.Fatal(err)
	}

	grownInput := append(slices.Clone(input), duplicateTestInput("cat again", "c", body)...)
	// A later occurrence with a different part count must not change the prefix.
	grown := project(append(grownInput, continuationTestOutput("b", duplicateTestHeader, body)))
	items := duplicateTestItems(t, grown)
	if got := binding.ExecutionOutputTexts(items[len(items)-1]["output"]); len(got) != 2 || got[0] != duplicateTestHeader || got[1] != "[same as O1]\n" {
		t.Fatalf("repeated output identity used the wrong part boundary: %q", got)
	}
	exchange := confirmedHistoryExchange(t, original, false)
	grown.Fields["previous_response_id"] = responses.MustMarshalJSON("parent")
	if _, err := exchange.Reconcile(grown, true); err != nil {
		t.Fatal(err)
	}
	if grown.CachedInput != len(original) || grown.RebaseInput {
		t.Fatal("growing projection rebased a confirmed prefix")
	}
}

func TestDuplicateOutputSourceIDs(t *testing.T) {
	a := "package router\n" + strings.Repeat("alpha source body\n", 20)
	b := "package router\n" + strings.Repeat("bravo source body\n", 20)
	c := "package router\n" + strings.Repeat("charlie source body\n", 20)
	frame := composer.FileFrames("/missing/source.go", a)[0]
	header, _, _ := strings.Cut(frame, "\n")
	input := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{frame})}}},
		continuationTestOutput("batch", duplicateTestHeader, b, c),
		continuationTestOutput("repeat", a, b, c),
	}
	request := &codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON(input)}}
	project := testInputProjections()[0].Prepare(request.Fields["input"])
	request.Fields["input"] = project(request.Fields["input"]).Input
	items := duplicateTestItems(t, request)
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(items[0]["content"], &parts); err != nil {
		t.Fatal(err)
	}
	frames, ok := attachments.Decode(parts[0].Text)
	if !ok || len(frames) != 1 || frames[0] != header+"\n[O1]\n"+a {
		t.Fatalf("attachment source = %q", frames)
	}
	if got, want := binding.ExecutionOutputTexts(items[1]["output"]), []string{duplicateTestHeader, "[O2]\n" + b, "[O3]\n" + c}; !slices.Equal(got, want) {
		t.Fatalf("sources with the same first line = %q, want %q", got, want)
	}
	if got, want := binding.ExecutionOutputTexts(items[2]["output"]), []string{"[same as O1]\n", "[same as O2]\n", "[same as O3]\n"}; !slices.Equal(got, want) {
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
	original := responses.MustMarshalJSON(input)
	request := &codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": original}}
	project := testInputProjections()[0].Prepare(request.Fields["input"])
	request.Fields["input"] = project(request.Fields["input"]).Input
	first := bytes.Clone(request.Fields["input"])
	items := duplicateTestItems(t, request)
	if responses.StringField(items[7], "output") != responses.StringField(items[9], "output") || strings.Contains(responses.StringField(items[9], "output"), "combined") {
		t.Fatal("new markers referenced a marker-only unit")
	}
	if responses.StringField(items[11], "output") != duplicateTestHeader+"[O4]\n"+literal || responses.StringField(items[13], "output") != duplicateTestHeader+"[same as O4]\n" {
		t.Fatal("literal host marker text did not remain a verbatim source")
	}
	request.Fields["input"] = original
	project = testInputProjections()[0].Prepare(original)
	request.Fields["input"] = project(request.Fields["input"]).Input
	if !bytes.Equal(first, request.Fields["input"]) {
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
		{"file snapshot", func(body string) string { return composer.FileFrames("/missing/file.go", body)[0] }},
		{"skill snapshot", func(body string) string { return composer.SkillFrames("review", "/missing/SKILL.md", body)[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.frame(body)
			header, _, ok := strings.Cut(frame, "\n")
			header += "\n"
			if !ok {
				t.Fatal("producer frame was not eligible")
			}
			attach := func(text string) any {
				return user(attachments.Encode([]string{text}))
			}
			input := []any{attach(frame), user(body), attach(frame)}
			for _, enabled := range []bool{false, true} {
				proxy := newManagedMekugiProxy(t)
				if enabled {
					proxy.inputProjections = testInputProjections()
				}
				wire := func(input []any) *codexresponses.Request {
					request := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
					if fresh := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{}); !bytes.Equal(request.Fields["input"], fresh.Fields["input"]) {
						t.Fatal("same original attachment input projected differently")
					}
					requestexecution.ProjectFileAttachments(request)
					return request
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
					t.Fatalf("wire content changed incorrectly: %s", request.Fields["input"])
				}
				if !responses.SameJSONValue(request.OriginalField("input"), responses.MustMarshalJSON(input)) {
					t.Fatal("original snapshot changed")
				}
				if !enabled || tc.name != "skill snapshot" {
					continue
				}
				first := request.Fields["input"]
				var prefix []jsonv1.RawMessage
				if err := json.Unmarshal(first, &prefix); err != nil {
					t.Fatal(err)
				}

				grown := wire(append(slices.Clone(input), attach(frame)))
				if texts(duplicateTestItems(t, grown)[3]) != want {
					t.Fatal("third snapshot referenced a generated marker")
				}
				exchange := confirmedHistoryExchange(t, prefix, false)
				grown.Fields["previous_response_id"] = responses.MustMarshalJSON("parent")
				if _, err := exchange.Reconcile(grown, true); err != nil {
					t.Fatal(err)
				}
				if grown.CachedInput != len(prefix) || grown.RebaseInput {
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
	frame := composer.FileFrames("/missing/example", body)[0]
	user := func(content any) any { return map[string]any{"role": "user", "content": content} }
	parts := []any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{frame})}, map[string]any{"type": "input_image", "image_url": "image"}}
	classified := map[string]any{"role": "user", "content": parts, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"agents_md.instructions", "user.image"}}}
	input := []any{
		classified,
		map[string]any{"role": "developer", "content": parts},
		user(attachments.Prefix + "invalid JSON" + attachments.Suffix),
		user("Attached file /missing: CONTENT NOT ATTACHED\n" + body),
		user(body),
		user([]any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{frame})}}),
	}
	input = append(input, duplicateTestInput("read file", "output", body)...)
	request := &codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON(input)}}
	project := testInputProjections()[0].Prepare(request.Fields["input"])
	request.Fields["input"] = project(request.Fields["input"]).Input
	items := duplicateTestItems(t, request)
	for at := range 5 {
		if !responses.SameJSONValue(responses.MustMarshalJSON(items[at]), responses.MustMarshalJSON(input[at])) {
			t.Fatalf("ineligible item %d changed", at)
		}
	}
	header, _, _ := strings.Cut(frame, "\n")
	header += "\n"
	source := user([]any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{header + "[O1]\n" + body})}})
	if !responses.SameJSONValue(responses.MustMarshalJSON(items[5]), responses.MustMarshalJSON(source)) {
		t.Fatal("eligible attachment source lost its anchor or verbatim body")
	}
	if got := responses.StringField(items[7], "output"); got != duplicateTestHeader+"[same as O1]\n" {
		t.Fatalf("tool did not reference the visible attachment: %q", got)
	}
}

func TestDuplicateOutputAttachmentDeliveredShape(t *testing.T) {
	var body string
	for i := range 12 {
		body += fmt.Sprintf("distinct delivered frame row %02d\n", i)
	}
	frame := composer.FileFrames("/missing/delivery", body)[0]
	envelope := attachments.Encode([]string{frame})
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
		proxy.inputProjections = testInputProjections()
		request := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
		requestexecution.ProjectFileAttachments(request)
		items := duplicateTestItems(t, request)
		// Unsupported shapes must not become invisible sources. Supported
		// envelopes move after ordinary parts, which must stay full and earlier.
		_, array := content.([]any)
		supported := array && len(content.([]any)) == 2
		if strings.Contains(responses.StringField(items[len(items)-1], "output"), "[same as") != supported {
			t.Fatalf("wrong source eligibility for %T: %s", content, request.Fields["input"])
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

func TestDuplicateOutputSummary(t *testing.T) {
	body := ""
	for i := range 20 {
		body += fmt.Sprintf("original line %d of output\n", i)
	}
	input := append(duplicateTestInput("a", "a", body), duplicateTestInput("b", "b", body)...)
	input = append(input, duplicateTestInput("c", "c", body+"new line\n")...)
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"input": input}))
	if err != nil {
		t.Fatal(err)
	}
	project := testInputProjections()[0].Prepare(request.Fields["input"])
	result := project(request.Fields["input"])
	request.Fields["input"] = result.Input
	summary := result.Receipt
	if summary.OutputRepetitions != 2 || summary.RepeatedCalls["b"] != "repeated" || summary.RepeatedCalls["c"] != "20 lines repeated" {
		t.Fatalf("summary = %+v", summary)
	}
	proxy := newManagedMekugiProxy(t)
	proxy.inputProjections = testInputProjections()
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = nativetrace.New(trace.root)
	source := `text((await tools.exec_command({cmd:"b"})).output)`
	trace.start("thread", "runtime", "b", source)
	trace.tool("thread", "runtime", "nested", "exec_command", `{"cmd":"b"}`)
	trace.result("thread", "nested", "completed", map[string]any{"exit_code": 0})
	trace.end("thread", "runtime")
	duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
	proxy.activity.Attach("thread")
	found := false
	for _, event := range takeNativeActivity(proxy.activity, "thread") {
		if event.Kind == "output_dedupe" && event.CallID == "nested" && event.Text == "repeated" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing native suffix receipt")
	}

}

func TestUISnapshotOutputDeduplicationTranscript(t *testing.T) {
	for _, label := range []string{"repeated", "20 lines repeated"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.shell.Transcript.Conversation = true
		u.testActivity(u.shell.Transcript).testAudience()
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": "command", "type": "commandExecution", "command": "cat file.txt", "status": "completed", "exitCode": 0, "aggregatedOutput": "original output\n"}})
		rollCommandOutput(u)
		applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{{Seq: u.attached.session.Activity().NextSequence(), Agent: "/root", Kind: "output_dedupe", CallID: "command", Text: label, Native: &sessionpresentation.NativeItem{Thread: "main"}}}, nil)
		got := strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(60, 20).Lines, "\n")
		if !strings.Contains(got, label) {
			t.Fatalf("missing suffix: %s", got)
		}
		assertNativeUISnapshot(t, "output-dedupe-"+strings.ReplaceAll(label, " ", "-"), strings.Split(got, "\n"))
	}
}
