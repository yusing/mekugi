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

	"github.com/coder/websocket"
)

// Exercise the provider wire, not the locally reconstructed tool catalog.
func TestWebSocketPrewarmToolGuidanceDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	proxy := newToolPluginTestProxy(t)
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	base := []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress}}
	incoming := []any{base[0], map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd}}
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
		upstream.SetReadLimit(upstreamJSONBufferBytes)
		defer upstream.CloseNow()
		var warmedTools json.RawMessage
		for index, id := range ids {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if bytes.Contains(request["input"], []byte("omitted-rtk-policy")) || bytes.Contains(request["input"], []byte("mekugi:omit")) || bytes.Contains(request["input"], []byte(conflictingProgress)) {
				t.Error("provider received omitted or conflicting instructions")
			}
			var input []json.RawMessage
			_ = json.Unmarshal(request["input"], &input)
			switch id {
			case "warm":
				warmedTools = bytes.Clone(request["tools"])
				if string(request["generate"]) != "false" || !bytes.Contains(request["input"], []byte("mekugi-journal:start")) ||
					!bytes.Contains(request["input"], []byte("mekugi-frontends:start")) || !bytes.Contains(request["input"], []byte("batch journal mutations")) ||
					!bytes.Contains(request["input"], []byte("tools.exec_command")) ||
					bytes.Contains(request["tools"], []byte(`"shell"`)) || !bytes.Contains(request["tools"], []byte(`"journal"`)) {
					t.Errorf("prewarm projection: generate=%s journal=%t frontends=%t conflict=%t stock=%t shell=%t tool=%t",
						request["generate"], bytes.Contains(request["input"], []byte("mekugi-journal:start")),
						bytes.Contains(request["input"], []byte("mekugi-frontends:start")), bytes.Contains(request["input"], []byte("batch journal mutations")),
						bytes.Contains(request["input"], []byte("tools.exec_command")), bytes.Contains(request["tools"], []byte(`"shell"`)), bytes.Contains(request["tools"], []byte(`"journal"`)))
				}
			case "turn":
				if !sameJSONValue(warmedTools, request["tools"]) {
					t.Error("first turn changed the warmed tool catalog")
				}
				if jsonString(request, "previous_response_id") != "warm" || len(input) != 1 {
					t.Errorf("first turn discarded warmed prefix: parent=%q items=%d", jsonString(request, "previous_response_id"), len(input))
				}
			case "next", "astra", "astra-next":
				if jsonString(request, "previous_response_id") != ids[index-1] || len(input) != 1 {
					t.Errorf("%s unnecessarily resent unchanged history", id)
				}
				if !sameJSONValue(warmedTools, request["tools"]) {
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
		request["client_metadata"] = map[string]string{codexTurnMetadataHeader: string(mustMarshalJSON(metadata)), threadIDHeader: "instruction-cache-thread"}
		socketWrite(t, ctx, conn, request)
		if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.completed" {
			t.Fatalf("%s failed: %s", id, mustMarshalJSON(event))
		}
	}
}

// The provider can reuse a prewarm only when the first turn repeats its
// projected instructions, tools, and leading items. Turn-only preparation after
// the prewarm return must not rewrite that prefix.
func TestPrewarmProjectionMatchesFirstTurnPrefix(t *testing.T) {
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "Code Mode", true: "native"}[native], func(t *testing.T) {
			proxy := newToolPluginTestProxy(t)
			leading := []any{
				map[string]any{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd},
				map[string]any{"type": "message", "role": "user", "content": "<environment_context>cwd</environment_context>"},
			}
			tools := []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
			if native {
				tools = append(testNativeResponsesTools(), tools...)
			} else {
				leading = append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, leading...)
			}
			fields := func(input []any) map[string]any {
				return map[string]any{
					"model": "gpt-test", "instructions": testBaseInstructions, "tools": tools, "tool_choice": "auto",
					"parallel_tool_calls": true, "reasoning": map[string]any{"effort": "high"}, "input": input,
				}
			}
			warmFields := fields(leading)
			warmFields["generate"] = false
			warm, err := parseResponsesRequest(mustTestJSON(t, warmFields))
			if err != nil {
				t.Fatal(err)
			}
			if transform, err := proxy.prepareModelRequest(t.Context(), &warm, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true); err != nil || transform != nil {
				t.Fatalf("prewarm projection: %v, %v", transform, err)
			}
			turn, err := parseResponsesRequest(mustTestJSON(t, fields(append(slices.Clone(leading), map[string]any{"type": "message", "role": "user", "content": "task"}))))
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

			for name, value := range warm.fields {
				if name == "input" || name == "generate" {
					continue
				}
				if !sameJSONValue(value, turn.fields[name]) {
					t.Errorf("first turn changed prewarmed %s:\nprewarm: %s\nturn:    %s", name, value, turn.fields[name])
				}
			}
			for name := range turn.fields {
				if _, present := warm.fields[name]; !present && name != "input" {
					t.Errorf("first turn added %s absent from the prewarm", name)
				}
			}
			var warmInput, turnInput []json.RawMessage
			if err := json.Unmarshal(warm.fields["input"], &warmInput); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(turn.fields["input"], &turnInput); err != nil {
				t.Fatal(err)
			}
			if len(turnInput) != len(warmInput)+1 {
				t.Fatalf("first turn input items = %d, want prewarm %d plus the user message", len(turnInput), len(warmInput))
			}
			for index := range warmInput {
				if !sameJSONValue(warmInput[index], turnInput[index]) {
					t.Errorf("first turn changed prewarmed input item %d:\nprewarm: %s\nturn:    %s", index, warmInput[index], turnInput[index])
				}
			}
			// The comparison is meaningful only if the prewarm was projected.
			if bytes.Contains(warm.fields["input"], []byte(conflictingProgress)) || !bytes.Contains(warm.fields["tools"], []byte(`"journal"`)) {
				t.Fatalf("prewarm skipped Mekugi projection: %s", mustMarshalJSON(warm.fields))
			}
		})
	}
}

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	exchange := &webSocketExchange{automatic: true, history: &webSocketHistory{}}
	request := &parsedResponsesRequest{cachedInput: 1}
	err := exchange.reconcileProviderHistory(request, []byte(`{"input":[{"role":"developer","content":"changed"}]}`))
	if err == nil || !strings.Contains(err.Error(), "cached provider history") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}

func TestPrewarmUnsupportedCatalogRemainsNative(t *testing.T) {
	proxy := newToolPluginTestProxy(t)
	request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","generate":false,"instructions":"native instructions","tools":[{"type":"web_search"}],"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	before := mustMarshalJSON(request.fields)
	transform, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true)
	if err != nil || transform != nil {
		t.Fatalf("native handshake initialized execution or failed: %v, %v", transform, err)
	}
	if !sameJSONValue(before, mustMarshalJSON(request.fields)) {
		t.Fatal("unsupported prewarm catalog was partially rewritten")
	}
}

func TestPrewarmMalformedCatalogRejectsWithoutPanic(t *testing.T) {
	proxy := newToolPluginTestProxy(t)
	for _, catalog := range []string{`[42]`, `"invalid"`, `[{"type":"function","name":"lookup"},42]`} {
		request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","generate":false,"tools":` + catalog + `,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true); err == nil {
			t.Fatalf("malformed catalog accepted: %s", catalog)
		}
	}
}
