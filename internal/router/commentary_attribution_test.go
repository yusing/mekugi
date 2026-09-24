package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChildCommentaryAttributionJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			workspace := t.TempDir()
			prepare := func(session, thread, author, kind string) *mekugiResponseTransform {
				t.Helper()
				request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}, "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}}))
				if err != nil {
					t.Fatal(err)
				}
				headers := http.Header{}
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, map[string]any{"request_kind": "turn", "agent_name": author, "subagent_kind": kind, "workspaces": map[string]any{workspace: nil}})))
				metadata, valid := decodeCodexTurnMetadata(headers)
				transform, err := proxy.prepareRequest(t.Context(), &request, session, thread, metadata, valid)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(transform.Close)
				return transform
			}
			// Keep both child requests active against the same proxy before transforming either.
			first := prepare("a", "a", "/root/alpha", "thread_spawn")
			second := prepare("b", "b", "/root/beta", "thread_spawn")
			root := prepare("root", "root", "/root/alpha", "")
			legacy := prepare("legacy", "legacy", "", "thread_spawn")
			for i, transform := range []*mekugiResponseTransform{first, second, root, legacy} {
				want := []string{
					"Journal update `/root/alpha` (`amber`)\nChecking.",
					"Journal update `/root/beta` (`amber`)\n[`/root/beta`] Checking.",
					"Journal update `/root` (`amber`)\nChecking.",
					"Journal update (`amber`)\nChecking.",
				}[i]
				arguments := `{"journal":[{"op":"add","text":"Checking.","report_now":true}]}`
				if i == 1 {
					arguments = string(mustTestJSON(t, map[string]any{"journal": []any{map[string]any{"op": "add", "text": "[`/root/beta`] Checking.", "report_now": true}}}))
				}
				call := map[string]any{"type": "function_call", "id": "item", "call_id": "call", "name": "lookup", "arguments": arguments}
				var output []byte
				if stream {
					events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}))
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						transform.Delivered(event)
					}
					output = bytes.Join(events, nil)
				} else {
					var err error
					output, err = transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
					if err != nil {
						t.Fatal(err)
					}
				}
				if !stream {
					transform.Delivered(output)
				}
				transform.ReleaseDelivery()
				if !bytes.Contains(output, mustTestJSON(t, want)) || bytes.Contains(output, []byte("] [")) {
					t.Fatalf("attribution: %s", output)
				}
				if i >= 2 && bytes.Contains(output, []byte("[`/root/")) {
					t.Fatalf("root/legacy relabeled: %s", output)
				}
				replay := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{map[string]any{"type": "function_call", "id": "item", "call_id": "call", "name": "lookup", "arguments": "{}"}})}}
				if err := proxy.reconcileInputPrefix(replay, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(replay.fields["input"], mustTestJSON(t, arguments)) {
					t.Fatalf("replay: %s", replay.fields["input"])
				}
			}
			// Code Mode captures the child author before execution and keeps it for live delivery.
			code := map[string]any{"type": "custom_tool_call", "name": second.codeModeToolName, "id": "code-item", "call_id": "code-call", "input": "await journal({op: 'add', text: 'Code work.'});"}
			if _, err := second.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": code})); err != nil {
				t.Fatal(err)
			}
			codeToken := runtimeCommentaryToken(t, second)
			proxy.commentary.publish(codeToken, "Code work.", false)
			publications := proxy.commentary.drain(codeToken)
			if len(publications) != 1 {
				t.Fatal("Code Mode publication missing")
			}
			message := second.runtimeCommentaryMessage(publications[0])
			if message == nil || !bytes.Contains(message["content"], []byte("[`/root/beta`] Code work.")) {
				t.Fatalf("Code Mode attribution: %s", mustTestJSON(t, message))
			}
			// A capability's author survives its creator and a later request with absent metadata.
			token := testRuntimeCommentaryCall(t, first, "deferred-code-call")
			first.Close()
			proxy.commentary.publish(token, "Deferred work.", false)
			next := prepare("a-next", "a", "", "thread_spawn")
			answer := map[string]any{"type": "message", "id": "answer", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Actual child answer."}}}
			var output []byte
			if stream {
				events, err := next.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer}))
				if err != nil {
					t.Fatal(err)
				}
				if len(events) != 0 {
					t.Fatal("answer was not buffered")
				}
				events, err = next.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{answer}}}))
				if err != nil {
					t.Fatal(err)
				}
				output = bytes.Join(events, nil)
			} else {
				var err error
				output, err = next.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{answer}}))
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Contains(output, []byte("[`/root/alpha`] Deferred work.")) || strings.LastIndex(string(output), "Actual child answer.") < strings.LastIndex(string(output), "Deferred work.") || !strings.Contains(string(output), "Actual child answer.") {
				t.Fatalf("missing deferred author/result ordering or provider answer: %s", output)
			}
			if events := proxy.drainCommentarySession(second.historySessionID, second.shellThreadID); len(events) != 0 {
				t.Fatal("publication crossed child sessions")
			}
		})
	}
}

func TestRuntimeCommentaryRenderedByteBudget(t *testing.T) {
	author := "/root/worker"
	prefix := "[" + commentaryCode(author) + "] "
	b := newCommentaryBroker()
	oversized := "/root/" + strings.Repeat("a", maxCommentaryPublicationBytes)
	if b.subscribe("session", "call", oversized) != "" {
		t.Fatal("oversized author admitted")
	}
	if len(b.routes) != 0 {
		t.Fatal("oversized author retained state")
	}

	token := b.subscribe("session", "call", author)
	if !b.publish(token, strings.Repeat("x", maxCommentaryPublicationBytes), true) {
		t.Fatal("oversized publication failed completion")
	}
	if len(b.routes) != 0 || b.eventCount != 0 {
		t.Fatal("oversized publication retained bytes or blocked completion")
	}

	token = b.subscribe("session", "call", author)
	fits := strings.Repeat("x", maxCommentaryPublicationBytes-len(prefix))
	b.publish(token, fits, false)
	b.publish(token, fits+"x", true)
	events := b.drain(token)
	if len(events) != 1 || len(events[0].text) != maxCommentaryPublicationBytes || events[0].text != prefix+fits || len(b.routes) != 0 {
		t.Fatal("rendered boundary or completion changed")
	}

}

func TestCommentaryCallRouteCapacityAndExpiry(t *testing.T) {
	broker := newCommentaryBroker()
	token := broker.subscribe("session", "call", "")
	if token == "" {
		t.Fatal("publisher route was not allocated")
	}
	for range maxCommentaryEventsPerRoute {
		if !broker.publish(token, "progress", false) {
			t.Fatal("active call route was retired")
		}
	}
	if events := broker.drain(token); len(events) != maxCommentaryEventsPerRoute {
		t.Fatalf("per-call pending cap = %d", len(events))
	}
	if !broker.publish(token, "over capacity", false) || len(broker.drain(token)) != 0 {
		t.Fatal("per-call lifetime capacity was not enforced")
	}
	broker.mu.Lock()
	broker.routes[token].expires = time.Now().Add(-time.Second)
	broker.mu.Unlock()
	if broker.publish(token, "expired", false) || len(broker.routes) != 0 || broker.eventCount != 0 {
		t.Fatal("expired call route retained capacity")
	}
	if replacement := broker.subscribe("session", "next-call", ""); replacement == "" || replacement == token {
		t.Fatal("expired route did not release capacity")
	}
}

func TestCallCommentaryDoesNotReclaimToolHistoryCapacity(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.commentaryEndpoint = "http://127.0.0.1" + commentaryPublisherPath
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	token := testRuntimeCommentaryCall(t, transform, "live-progress-call")

	// Fill four history sessions to their independent budget limits while
	// retaining the authenticated call that owns this publisher.
	sessions := []string{transform.historySessionID, "filler-one", "filler-two", "filler-three"}
	for _, sessionID := range sessions {
		used := 0
		if session := proxy.sessions[sessionID]; session != nil {
			used = session.bytes
		}
		script := strings.Repeat("x", maxMekugiHistorySessionBytes-used-256)
		if err := proxy.rememberBatch(sessionID, map[string]mekugiHistory{
			"essential": {Script: script},
		}); err != nil {
			t.Fatalf("fill history session %q: %v", sessionID, err)
		}
	}
	before := proxy.historyBytes
	for range maxCommentaryEventsPerRoute {
		proxy.commentary.publish(token, "auxiliary", false)
		publication := proxy.commentary.drain(token)
		if len(publication) != 1 || transform.runtimeCommentaryMessage(publication[0]) == nil {
			t.Fatal("call-scoped commentary unavailable while tool history is full")
		}
	}
	if proxy.historyBytes != before {
		t.Fatal("commentary consumed essential history budget")
	}
	for _, sessionID := range sessions {
		if _, exists := proxy.history(sessionID, "essential"); !exists {
			t.Fatalf("commentary evicted essential history in %q", sessionID)
		}
	}
	if err := proxy.rememberBatch(transform.historySessionID, map[string]mekugiHistory{"later": {Script: "ok"}}); err != nil {
		t.Fatalf("later tool admission blocked: %v", err)
	}
	if _, exists := proxy.history(transform.historySessionID, "essential"); !exists {
		t.Fatal("commentary impaired later tool admission")
	}
}
