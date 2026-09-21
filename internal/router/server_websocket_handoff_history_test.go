package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestResponsesWebSocketHandoffPreservesStreamedCallWithActivity(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	proxy := newToolPluginTestProxy(t)
	const thread = "handoff-history"
	proxy.activity.observe(thread, "", "/root", false)
	proxy.activity.observe("child", thread, "/root/child", true)
	call := map[string]string{
		"type": "custom_tool_call", "id": "shell-item", "call_id": "shell-call",
		"name": "shell", "input": "printf ok", "status": "completed",
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		first, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(first, "model") != "gpt-6-astra" {
			t.Errorf("mentor model = %q", jsonString(first, "model"))
		}
		proxy.activity.collect("child", "progress", "commentary", "Inspecting child files.")
		for _, event := range []map[string]any{
			socketEvent("response.created", "first"),
			{"type": "response.output_item.done", "output_index": 0, "item": call},
			// An empty terminal output must not erase the streamed call when
			// activity commentary is inserted into the downstream snapshot.
			{"type": "response.completed", "response": map[string]any{
				"id": "first", "status": "completed", "output": []any{},
				"usage": map[string]int{"input_tokens": 50_000, "output_tokens": 10},
			}},
		} {
			if err := providerSocketWrite(ctx, upstream, event); err != nil {
				t.Error(err)
				return
			}
		}
		next, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(next, "model") != "gpt-5.6-sol" || jsonString(next, "previous_response_id") != "first" {
			t.Errorf("handoff did not retain provider prefix: model=%q parent=%q", jsonString(next, "model"), jsonString(next, "previous_response_id"))
		}
		var input []map[string]json.RawMessage
		if err := json.Unmarshal(next["input"], &input); err != nil {
			t.Error(err)
			return
		}
		if len(input) != 1 || jsonString(input[0], "type") != "custom_tool_call_output" ||
			jsonString(input[0], "call_id") != "shell-call" || jsonString(input[0], "output") != "ok" ||
			bytes.Contains(next["input"], []byte("Inspecting child files.")) {
			t.Errorf("provider prefix continuation lost its result or leaked activity: %s", next["input"])
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "second")); err != nil {
			t.Error(err)
		}
		_, _, _ = upstream.Read(ctx)
	}))
	t.Cleanup(provider.Close)
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(provider.URL, provider.Client()), nil, proxy, newMentorHandoff(true, true))
	t.Cleanup(endpoint.Close)
	server := httptest.NewServer(endpoint)
	t.Cleanup(server.Close)
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, thread)
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	metadata := map[string]string{
		codexTurnMetadataHeader: string(mustMarshalJSON(codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil}})),
		threadIDHeader:          thread,
	}
	socketWrite(t, ctx, conn, map[string]any{
		"type": "response.create", "model": "gpt-5.6-sol", "client_metadata": metadata,
		"input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task."}},
	})
	seenCall, seenActivity := false, false
	for {
		event := socketRead(t, ctx, conn)
		if jsonString(event, "type") == "error" {
			t.Fatalf("first response failed: %s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.output_item.done" {
			var item map[string]json.RawMessage
			_ = json.Unmarshal(event["item"], &item)
			seenCall = seenCall || jsonString(item, "call_id") == "shell-call" && jsonString(item, "name") == "exec"
			seenActivity = seenActivity || strings.Contains(string(event["item"]), "Inspecting child files.")
		}
		if jsonString(event, "type") == "response.completed" {
			break
		}
	}
	if !seenCall || !seenActivity {
		t.Fatalf("missing downstream call or activity: call=%v activity=%v", seenCall, seenActivity)
	}
	socketWrite(t, ctx, conn, map[string]any{
		"type": "response.create", "model": "gpt-5.6-sol", "client_metadata": metadata, "previous_response_id": "first",
		"input": []any{map[string]string{"type": "custom_tool_call_output", "call_id": "shell-call", "output": "ok"}},
	})
	for {
		event := socketRead(t, ctx, conn)
		if jsonString(event, "type") == "error" {
			t.Fatalf("continuation failed: %s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.completed" {
			break
		}
	}
}
