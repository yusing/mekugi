package router

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestResponsesWebSocketIncrementalTranslationAndVisibleSources(t *testing.T) {
	proxy := newToolPluginTestProxy(t)
	proxy.customizedInstructions = true
	proxy.compactModelProtocol = true
	proxy.activity.copies["router-only"] = struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "socket-session")
	maps.Copy(headers, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil}))
	source := "a native output line that remains available on the same connection\n"
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		upstream.SetReadLimit(upstreamJSONBufferBytes)
		defer upstream.CloseNow()
		first, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(first["tools"]), `"plugin_tool"`) || strings.Contains(string(first["input"]), "router-only") {
			t.Errorf("request projection missing: %s", mustMarshalJSON(first))
		}
		item := map[string]string{"type": "custom_tool_call", "id": "item", "call_id": "plugin-call", "name": "plugin_tool", "input": "function", "status": "completed"}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "first")); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{"id": "first", "status": "completed", "output": []any{item}}}); err != nil {
			t.Error(err)
			return
		}
		next, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		var input []map[string]json.RawMessage
		_ = json.Unmarshal(next["input"], &input)
		if jsonString(next, "previous_response_id") != "first" || len(input) != 1 || jsonString(input[0], "type") != "custom_tool_call_output" || jsonString(input[0], "output") != "result" {
			t.Errorf("incremental carrier restoration or prefix filtering failed: %s", mustMarshalJSON(next))
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "second")); err != nil {
			t.Error(err)
			return
		}
		message := map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "commentary", "status": "completed", "content": []any{
			map[string]any{"type": "output_text", "text": "!V=source,1,1\n", "annotations": []any{}},
		}}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{"id": "second", "status": "completed", "output": []any{message}}}); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, mustCTP2Codec(t), headers)
	socketWrite(t, ctx, conn, map[string]any{
		"type": "response.create", "model": "gpt-test", "instructions": "Follow the task.",
		"input": []any{
			testCodeModeAdditionalTools(testCodeModeDescription),
			map[string]any{"type": "message", "id": "router-only", "role": "assistant", "content": "router-only"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_source", "output": source},
			map[string]string{"role": "user", "content": "task"},
		},
		"tools": []any{map[string]string{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
	})
	var terminal map[string]json.RawMessage
	for {
		event, err := providerSocketRead(ctx, conn)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(event, "type") == "error" {
			t.Fatalf("translation error: %s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.completed" {
			terminal = event
			break
		}
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(terminal["response"], &response)
	if len(response.Output) == 0 || jsonString(response.Output[0], "type") != "function_call" || jsonString(response.Output[0], "name") != "lookup" {
		t.Fatalf("plugin call not restored: %s", terminal["response"])
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "instructions": "Follow the task.", "tools": []any{map[string]string{"type": "function", "name": "lookup"}}, "tool_choice": "auto", "previous_response_id": "first", "input": []any{
		map[string]string{"type": "function_call_output", "call_id": "plugin-call", "output": "result"},
	}})
	for {
		event, err := providerSocketRead(ctx, conn)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(event, "type") == "error" {
			t.Fatalf("continuation error: %s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.completed" {
			if strings.Contains(string(event["response"]), "!V=") || !strings.Contains(string(event["response"]), strings.TrimSpace(source)) {
				t.Fatalf("cached CTP source was not restored: %s", event["response"])
			}
			break
		}
	}
}
