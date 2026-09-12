package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestChildCommentaryAttributionJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
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
					"Journal update `/root/alpha` (`j1`)\nChecking.",
					"Journal update `/root/beta` (`j1`)\n[`/root/beta`] Checking.",
					"Journal update `/root` (`j1`)\nChecking.",
					"Journal update (`j1`)\nChecking.",
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
			first.Close()
			token := proxy.commentary.subscribeThread(first.historySessionID, "a", "/root/alpha")
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
	if b.subscribe("session", "call", oversized) != "" || b.subscribeThread("session", "thread", oversized) != "" {
		t.Fatal("oversized author admitted")
	}
	if len(b.routes) != 0 || len(b.threads) != 0 {
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

	token = b.subscribeThread("session", "thread", author)
	b.publish(token, prefix+fits, true) // Already attributed text fits exactly.
	b.publish(token, fits+"x", true)
	events = b.drain(token)
	if len(events) != 1 || events[0].text != prefix+fits || b.threadIDCount != 1 {
		t.Fatal("prefix or provenance budget changed")
	}
	if !b.hasThreadMessageID("thread", events[0].messageID) {
		t.Fatal("accepted message lost replay provenance")
	}
	if !b.publish(token, "Still active.", false) || len(b.drain(token)) != 1 {
		t.Fatal("shared thread completion retired publisher")
	}
}

func TestOversizedRuntimeAuthorPreservesOperationResult(t *testing.T) {
	transform, proxy := newRuntimeCommentaryTransform(t)
	transform.commentaryAuthor = "/root/" + strings.Repeat("a", maxCommentaryPublicationBytes)
	call := map[string]any{"type": "custom_tool_call", "name": transform.codeModeToolName, "id": "code", "call_id": "call", "input": "await commentary('progress'); text('actual result');"}
	output, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(transform.commentarySubscriptions) != 0 || len(proxy.commentary.routes) != 0 {
		t.Fatal("oversized author retained runtime capability")
	}
	if !bytes.Contains(output, []byte("actual result")) || bytes.Contains(output, []byte(commentaryOnceArgument)) {
		t.Fatalf("operation changed: %s", output)
	}
	history := transform.local["call"]
	if history.script != "await commentary('progress'); text('actual result');" {
		t.Fatal("original replay source changed")
	}
}
