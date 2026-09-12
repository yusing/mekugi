package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func prepareActivityTest(t *testing.T, proxy *mekugiProxy, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	tools := []any{
		map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
			map[string]any{"type": "function", "name": "send_message"}, map[string]any{"type": "function", "name": "followup_task"},
		}},
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...), "tools": tools}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexTurnMetadata{RequestKind: "turn", ThreadID: thread, ParentThreadID: parent, AgentName: name}
	if parent != "" {
		metadata.SubagentKind = "thread_spawn"
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, session, thread, metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, &request
}

func TestRejectedChildIdentityDoesNotReuseEarlierAttribution(t *testing.T) {
	for _, parent := range []string{"", "child"} {
		t.Run("parent="+parent, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child-session", "child", "root", "/root/alpha", nil)
			child.Close()
			root.drainActivity()
			request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}, "tools": []any{},
			}))
			if err != nil {
				t.Fatal(err)
			}
			next, err := proxy.prepareRequest(t.Context(), &request, "next-session", "child", codexTurnMetadata{
				RequestKind: "turn", ThreadID: "child", ParentThreadID: parent,
				AgentName: "/root/beta", SubagentKind: "thread_spawn",
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(next.Close)
			message := assistantCommentaryMessage("new-progress", "New child progress.")
			output, err := next.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{message}}))
			if err != nil || !bytes.Contains(output, []byte("New child progress.")) {
				t.Fatal("child output changed", string(output), err)
			}
			if got := root.drainActivity(); len(got) != 0 {
				t.Fatal("rejected identity reused old attribution", got)
			}
			if proxy.activity.observe("child", "root", "/root/alpha", true) {
				t.Fatal("conflicting identity was not retained")
			}
		})
	}
}

func TestActualChildActivityProjectsWithoutChangingChildResult(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			p := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			p.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			root, _ := prepareActivityTest(t, p, "root-session", "root-thread", "", "/root", nil)
			other, _ := prepareActivityTest(t, p, "other-session", "other-root", "", "/root", nil)
			parent, _ := prepareActivityTest(t, p, "child-session", "child-thread", "root-thread", "/root/alpha", nil)
			child, _ := prepareActivityTest(t, p, "nested-session", "nested-thread", "child-thread", "/root/alpha/nested", nil)
			call := map[string]any{"type": "function_call", "id": "lookup", "call_id": "lookup", "name": "lookup", "arguments": `{"journal":[{"op":"add","text":"Checking cancellation.","report_now":true}]}`}
			childResponse := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
			childOutput, err := child.TransformJSON(childResponse)
			if err != nil {
				t.Fatal(err)
			}
			child.Delivered(childOutput)
			child.ReleaseDelivery()
			rootResponse := mustTestJSON(t, map[string]any{"id": "root-response", "status": "completed", "output": []any{assistantCommentaryMessage("answer", "Substantive result.")}})
			var projected []byte
			if stream {
				events, err := root.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(rootResponse)}))
				if err != nil {
					t.Fatal(err)
				}
				projected = bytes.Join(events, nil)
			} else {
				var err error
				projected, err = root.TransformJSON(rootResponse)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Contains(projected, mustTestJSON(t, "[`/root/alpha/nested`] Journal update `/root/alpha/nested` (`j1`)\nChecking cancellation.")) || !bytes.Contains(projected, []byte("Substantive result.")) {
				t.Fatal(string(projected))
			}
			untouched, err := other.TransformJSON(rootResponse)
			if err != nil || bytes.Contains(untouched, []byte("Checking cancellation")) {
				t.Fatal(string(untouched), err)
			}
			// A publication after both child and root response lifetimes keeps ancestry,
			// regardless of the routing session used by the next root request.
			token := p.commentary.subscribeThread(child.historySessionID, "nested-thread", "/root/alpha/nested")
			root.Close()
			child.Close()
			parent.Close()
			p.commentary.publish(token, "Late runtime progress.", false)
			next, request := prepareActivityTest(t, p, "remapped-session", "root-thread", "", "/root", nil)
			output, err := next.TransformJSON(rootResponse)
			if err != nil || bytes.Contains(output, []byte("since the last update")) || !bytes.Contains(output, []byte("[`/root/alpha/nested`] Late runtime progress.")) {
				t.Fatal(string(output), err)
			}
			var response struct{ Output []map[string]json.RawMessage }
			_ = json.Unmarshal(output, &response)
			request.fields["input"] = mustMarshalJSON(response.Output)
			p.activity.stripInput(request.fields)
			if bytes.Contains(request.fields["input"], []byte("Late runtime")) || !bytes.Contains(request.fields["input"], []byte("Substantive result.")) {
				t.Fatal(string(request.fields["input"]))
			}
			// Root projection did not consume the child's own runtime publication.
			publications := p.commentary.drain(token)
			if len(publications) != 1 || !strings.Contains(publications[0].text, "Late runtime") {
				t.Fatal(publications)
			}
		})
	}
}

func TestSiblingReceiptAndOpaqueCallsKeepExactEnvelope(t *testing.T) {
	p := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	root, _ := prepareActivityTest(t, p, "root", "r", "", "/root", nil)
	body := strings.Repeat("evidence ", 100)
	envelope := map[string]any{"type": "agent_message", "id": "envelope", "author": "/root/alpha", "recipient": "/root/beta", "content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root/beta\nSender: /root/alpha\nPayload:\n" + body}}}
	child, request := prepareActivityTest(t, p, "child", "b", "r", "/root/beta", []any{envelope})
	if !bytes.Contains(request.fields["input"], []byte(body)) {
		t.Fatal("original reply shortened")
	}
	call := map[string]any{"type": "function_call", "namespace": "collaboration", "name": "followup_task", "call_id": "follow", "arguments": `{"target":"/root/alpha","message":"opaque-secret"}`}
	output, err := child.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("Follow-up requested.")) || !bytes.Contains(output, []byte("opaque-secret")) || !bytes.Contains(output, []byte(body)) || bytes.Contains(output, []byte("[excerpt]")) {
		t.Fatal(string(output))
	}
	output, err = root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
	if err != nil || !bytes.Contains(output, []byte("`/root/beta` <- `/root/alpha`")) || bytes.Contains(output, []byte("Follow-up requested.")) || bytes.Contains(output, []byte("opaque-secret")) || bytes.Contains(output, []byte("] [")) {
		t.Fatal(string(output), err)
	}
}

func TestActivityCapacityDoesNotRejectToolsAndOpaqueReceipt(t *testing.T) {
	p := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	p.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	root, _ := prepareActivityTest(t, p, "root", "r", "", "/root", nil)
	envelope := map[string]any{"type": "agent_message", "id": "opaque", "author": "/root/a", "recipient": "/root/b", "content": []any{map[string]any{"type": "encrypted_content", "encrypted_content": "opaque-secret"}}}
	child, request := prepareActivityTest(t, p, "child", "b", "r", "/root/b", []any{envelope})
	if !bytes.Contains(request.fields["input"], []byte("opaque-secret")) {
		t.Fatal("original opaque message removed")
	}
	visible, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
	if err != nil || !bytes.Contains(visible, []byte("Message received.")) || bytes.Contains(visible, []byte("opaque-secret")) {
		t.Fatal(string(visible), err)
	}
	p.activity.mu.Lock()
	p.activity.sources = maxThreadCommentaryIDs
	p.activity.mu.Unlock()
	call := map[string]any{"type": "function_call", "name": "lookup", "call_id": "capacity-call", "arguments": `{"commentary":"Useful work"}`}
	visible, err = child.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
	if err != nil || !bytes.Contains(visible, []byte(`"call_id":"capacity-call"`)) {
		t.Fatal(string(visible), err)
	}
	if len(p.activity.drain("r", root.activityStarted, maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("capacity did not suppress projection")
	}
}

func TestFirstChildRequestStripsInheritedRootCopies(t *testing.T) {
	p := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	root, _ := prepareActivityTest(t, p, "root-session", "r", "", "/root", nil)
	p.activity.observe("c", "r", "/root/existing", true)
	p.activity.collect("c", "actual", "reply", "display-only real activity")
	p.activity.collect("c", "second", "reply", "display-only second activity")
	output, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct{ Output []map[string]json.RawMessage }
	if err := json.Unmarshal(output, &response); err != nil || len(response.Output) != 2 {
		t.Fatal(string(output), err)
	}
	input := []any{response.Output[0], response.Output[1], assistantCommentaryMessage("original-child", "original substantive reply")}
	_, request := prepareActivityTest(t, p, "new-child-session", "first-child", "r", "/root/first_child", input)
	if bytes.Contains(request.fields["input"], []byte("display-only")) || !bytes.Contains(request.fields["input"], []byte("original substantive reply")) {
		t.Fatal(string(request.fields["input"]))
	}
}
