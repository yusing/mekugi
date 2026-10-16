package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Exercise the provider wire, not the locally reconstructed tool catalog.
func TestWebSocketPrewarmToolGuidanceDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	proxy := newManagedMekugiProxy(t)
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	stock, _ := recordedInstructionCleanup(t)
	base := []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock}}
	incoming := []any{base[0], map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock + "\n" + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd}}
	ids := []string{"warm", "turn", "next", "astra", "astra-next"}
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "instruction-cache-session")
	directory := t.TempDir()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		upstream.SetReadLimit(codexresponses.MaxResponseBytes)
		defer upstream.CloseNow()
		var warmedTools json.RawMessage
		for index, id := range ids {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if bytes.Contains(request["input"], []byte("omitted-rtk-policy")) || bytes.Contains(request["input"], []byte("mekugi:omit")) || bytes.Contains(request["input"], []byte(conflictingProgress)) || bytes.Contains(request["input"], []byte("recommended_plugins")) {
				t.Error("provider received omitted or conflicting instructions")
			}
			var input []json.RawMessage
			_ = json.Unmarshal(request["input"], &input)
			switch id {
			case "warm":
				warmedTools = bytes.Clone(request["tools"])
				if string(request["generate"]) != "false" ||
					!bytes.Contains(request["input"], []byte("mekugi-frontends:start")) ||
					!bytes.Contains(request["input"], []byte("tools.exec_command")) ||
					bytes.Contains(request["tools"], []byte(`"shell"`)) {
					t.Errorf("prewarm projection: generate=%s frontends=%t stock=%t shell=%t",
						request["generate"],
						bytes.Contains(request["input"], []byte("mekugi-frontends:start")),
						bytes.Contains(request["input"], []byte("tools.exec_command")), bytes.Contains(request["tools"], []byte(`"shell"`)))
				}
			case "turn":
				if !responses.SameJSONValue(warmedTools, request["tools"]) {
					t.Error("first turn changed the warmed tool catalog")
				}
				if responses.StringField(request, "previous_response_id") != "warm" || len(input) != 1 {
					t.Errorf("first turn discarded warmed prefix: parent=%q items=%d", responses.StringField(request, "previous_response_id"), len(input))
				}
			case "next", "astra", "astra-next":
				if responses.StringField(request, "previous_response_id") != ids[index-1] || len(input) != 1 {
					t.Errorf("%s unnecessarily resent unchanged history", id)
				}
				if !responses.SameJSONValue(warmedTools, request["tools"]) {
					t.Errorf("%s changed model-independent tool guidance", id)
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	for index, id := range ids {
		model := "gpt-5.6-luna"
		if index >= 3 {
			model = "gpt-6-astra"
		}
		request := map[string]any{"type": "response.create", "model": model}
		metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{directory: nil}}
		if index == 0 {
			metadata.RequestKind = "prewarm"
			request["generate"] = false
			request["input"] = incoming
		} else {
			request["previous_response_id"] = ids[index-1]
			request["input"] = []any{map[string]string{"role": "user", "content": id}}
		}
		request["client_metadata"] = map[string]string{codexresponses.TurnMetadataHeader: string(responses.MustMarshalJSON(metadata)), codexresponses.ThreadIDHeader: "instruction-cache-thread"}
		socketWrite(t, ctx, conn, request)
		if event := socketRead(t, ctx, conn); responses.StringField(event, "type") != "response.completed" {
			t.Fatalf("%s failed: %s", id, responses.MustMarshalJSON(event))
		}
	}
}

// The provider can reuse a prewarm only when the first turn repeats its
// projected instructions, tools, and leading items. Turn-only preparation after
// the prewarm return must not rewrite that prefix.
func TestPrewarmProjectionMatchesFirstTurnPrefix(t *testing.T) {
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	stock, projected := recordedInstructionCleanup(t)
	for _, topLevel := range []bool{false, true} {
		t.Run(map[bool]string{false: "additional tools", true: "top-level tools"}[topLevel], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			leading := []any{
				map[string]any{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock + "\n" + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd},
				map[string]any{"type": "message", "role": "user", "content": "<environment_context>cwd</environment_context>"},
			}
			tools := []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
			if topLevel {
				tools = append(testExecResponsesTools(), tools...)
			} else {
				leading = append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, leading...)
			}
			fields := func(input []any) map[string]any {
				return map[string]any{
					"model": "gpt-test", "instructions": testBaseInstructions + "\n" + stock, "tools": tools, "tool_choice": "auto",
					"parallel_tool_calls": true, "reasoning": map[string]any{"effort": "high"}, "input": input,
				}
			}
			warmFields := fields(leading)
			warmFields["generate"] = false
			warm, err := codexresponses.ParseRequest(mustTestJSON(t, warmFields))
			if err != nil {
				t.Fatal(err)
			}
			if transform, err := proxy.prepareModelRequest(t.Context(), &warm, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true); err != nil || transform != nil {
				t.Fatalf("prewarm projection: %v, %v", transform, err)
			}
			turn, err := codexresponses.ParseRequest(mustTestJSON(t, fields(append(slices.Clone(leading), map[string]any{"type": "message", "role": "user", "content": "task"}))))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := proxy.prepareRequest(t.Context(), &turn, "prefix-session", "prefix-thread", codexTurnMetadata{
				RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil},
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(transform.Close)

			for name, value := range warm.Fields {
				if name == "input" || name == "generate" {
					continue
				}
				if !responses.SameJSONValue(value, turn.Fields[name]) {
					t.Errorf("first turn changed prewarmed %s:\nprewarm: %s\nturn:    %s", name, value, turn.Fields[name])
				}
			}
			for name := range turn.Fields {
				if _, present := warm.Fields[name]; !present && name != "input" {
					t.Errorf("first turn added %s absent from the prewarm", name)
				}
			}
			var warmInput, turnInput []json.RawMessage
			if err := json.Unmarshal(warm.Fields["input"], &warmInput); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(turn.Fields["input"], &turnInput); err != nil {
				t.Fatal(err)
			}
			if len(turnInput) != len(warmInput)+1 {
				t.Fatalf("first turn input items = %d, want prewarm %d plus the user message", len(turnInput), len(warmInput))
			}
			for index := range warmInput {
				if !responses.SameJSONValue(warmInput[index], turnInput[index]) {
					t.Errorf("first turn changed prewarmed input item %d:\nprewarm: %s\nturn:    %s", index, warmInput[index], turnInput[index])
				}
			}
			if got := responses.StringField(warm.Fields, "instructions"); got != testBaseInstructions+"\n"+projected {
				t.Fatalf("effective instruction cleanup = %q", got)
			}
			for _, raw := range warmInput {
				var item map[string]json.RawMessage
				_ = json.Unmarshal(raw, &item)
				if responses.StringField(item, "type") == "message" && responses.StringField(item, "role") == "developer" && responses.StringField(item, "content") != "Follow the task.\n\n"+projected+"\n"+instructionOmitStart+"omitted-rtk-policy"+instructionOmitEnd {
					t.Fatal("effective developer cleanup differs from recorded projection")
				}
			}
			// The comparison is meaningful only if the prewarm was projected.
			if bytes.Contains(warm.Fields["input"], []byte(conflictingProgress)) || !bytes.Contains(append(slices.Clone(warm.Fields["tools"]), warm.Fields["input"]...), []byte(frontendGuidanceStart)) {
				t.Fatalf("prewarm skipped Mekugi projection: %s", responses.MustMarshalJSON(warm.Fields))
			}
		})
	}
}

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	var session codexresponses.SessionHistory
	parent, _, err := session.Prepare([]byte(`{"input":[]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.ObserveOutput([]byte(`{"type":"response.completed","response":{"id":"parent","output":[]}}`)); err != nil {
		t.Fatal(err)
	}
	exchange, _, err := session.Prepare(nil, []byte(`{"type":"response.created","response":{"id":"next"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request := &codexresponses.Request{CachedInput: 1, Fields: map[string]json.RawMessage{"input": json.RawMessage(`[{"role":"developer","content":"changed"}]`)}}
	_, err = exchange.Reconcile(request, true)
	if err == nil || !strings.Contains(err.Error(), "cached provider history") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}

func TestPrewarmUnsupportedCatalogRemainsNative(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	stock, _ := recordedInstructionCleanup(t)
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "generate": false, "instructions": stock, "tools": []any{map[string]string{"type": "web_search"}}, "input": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	before := responses.MustMarshalJSON(request.Fields)
	transform, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true)
	if err != nil || transform != nil {
		t.Fatalf("native handshake initialized execution or failed: %v, %v", transform, err)
	}
	if !responses.SameJSONValue(before, responses.MustMarshalJSON(request.Fields)) {
		t.Fatal("unsupported prewarm catalog was partially rewritten")
	}
}

func TestPrewarmMalformedCatalogRejectsWithoutPanic(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	for _, catalog := range []string{`[42]`, `"invalid"`, `[{"type":"function","name":"lookup"},42]`} {
		request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","generate":false,"tools":` + catalog + `,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true); err == nil {
			t.Fatalf("malformed catalog accepted: %s", catalog)
		}
	}
}
