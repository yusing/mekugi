package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestThreadCommentarySharedCompletionAndSessionMapping(t *testing.T) {
	b := newCommentaryBroker()
	token := b.subscribeThread("session-a", "thread-a", "")
	other := b.subscribeThread("session-b", "thread-b", "")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() { b.publish(token, fmt.Sprint(i), true) })
	}
	wg.Wait()
	if token == "" || token == other || b.subscribeThread("session-new", "thread-a", "") != token {
		t.Fatal("thread capability was not stable and isolated")
	}
	if events := b.drainSession("session-a", "thread-a"); len(events) != 0 {
		t.Fatal("old session received remapped thread publications")
	}
	events := b.drainSession("session-new", "thread-a")
	if len(events) != 20 {
		t.Fatalf("events = %d", len(events))
	}
	seen := make(map[string]bool)
	for _, event := range events {
		if event.callID != "" || seen[event.messageID] {
			t.Fatal("thread publication borrowed a call or repeated identity")
		}
		seen[event.messageID] = true
	}
	if !b.publish(token, "after shared completion", true) || len(b.drainSession("session-new", "thread-a")) != 1 {
		t.Fatal("worker completion retired shared thread")
	}
	if len(b.drainSession("session-b", "thread-b")) != 0 {
		t.Fatal("thread isolation failed")
	}
}

func TestThreadCommentaryCapacityRefreshAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newCommentaryBroker()
		token := b.subscribeThread("session", "thread", "")
		for range maxCommentaryEventsPerRoute + 1 {
			b.publish(token, "pending", false)
		}
		if events := b.drain(token); len(events) != maxCommentaryEventsPerRoute {
			t.Fatalf("pending cap = %d", len(events))
		}
		for range maxCommentaryEventsPerRoute + 1 {
			b.publish(token, "ongoing", true)
			if len(b.drain(token)) != 1 {
				t.Fatal("lifetime publication count capped thread")
			}
		}
		for i := range maxCommentaryRoutes - 1 {
			b.subscribeThread("session", fmt.Sprint(i), "")
		}
		if b.subscribeThread("session", "overflow", "") != "" {
			t.Fatal("route capacity not bounded")
		}
		time.Sleep(commentaryRouteTTL / 2)
		if b.subscribeThread("session", "thread", "") != token {
			t.Fatal("full capacity prevented refresh")
		}
		time.Sleep(commentaryRouteTTL / 2)
		if !b.publish(token, "refreshed", false) {
			t.Fatal("refreshed capability expired early")
		}
		time.Sleep(commentaryRouteTTL)
		if len(b.drain(token)) != 0 || b.eventCount != 0 || len(b.routes) != 0 {
			t.Fatal("expiry retained capacity")
		}
		if replacement := b.subscribeThread("session", "thread", ""); replacement == "" || replacement == token {
			t.Fatal("expiry did not rotate capability")
		}
	})
}

func TestThreadCommentaryTerminalReplayWithoutCallHistory(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			token := proxy.commentary.subscribeThread(transform.historySessionID, transform.shellThreadID, "")
			proxy.commentary.publish(token, "thread progress", true)
			events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response." + status, "response": map[string]any{"status": status, "output": []any{}},
			}))
			if err != nil || len(events) != 2 || !bytes.Contains(events[0], []byte("thread progress")) {
				t.Fatalf("terminal events = %s, %v", events, err)
			}
			ids := proxy.commentaryMessageIDs(transform.historySessionID)
			if len(ids) != 1 {
				t.Fatalf("retained IDs = %v", ids)
			}
			var id string
			for value := range ids {
				id = value
			}
			if proxy.sessions[transform.historySessionID] != nil || proxy.historyBytes != 0 {
				t.Fatal("thread replay consumed tool history budget")
			}

			generated := assistantCommentaryMessage(id, "thread progress")
			other := assistantCommentaryMessage(commentaryMessageID("unretained"), "keep")
			request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{generated, other})}}
			if err := proxy.reconcileInputPrefix(request, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(request.fields["input"], []byte(id)) || !bytes.Contains(request.fields["input"], []byte("keep")) {
				t.Fatalf("replay = %s", request.fields["input"])
			}
			if len(proxy.commentaryMessageIDs("unrelated")) != 0 {
				t.Fatal("replay IDs crossed sessions")
			}
			transform.Close()
			proxy.commentary.publish(token, "later", true)
			if events := proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID); len(events) != 1 || events[0].text != "later" {
				t.Fatalf("deferred events = %+v", events)
			}
		})
	}
}

func TestThreadCommentaryReplaySurvivesSessionRemapAndExpiry(t *testing.T) {
	transform, proxy := newRuntimeCommentaryTransform(t)
	transform.shellThreadID = "stable-thread"
	oldSession := transform.historySessionID
	token := proxy.commentary.subscribeThread(oldSession, "stable-thread", "")
	proxy.commentary.publish(token, "delivered", false)
	publication := proxy.commentary.drainSession(oldSession, "stable-thread")[0]
	message := transform.runtimeCommentaryMessage(publication)
	if message == nil {
		t.Fatal("initial publication suppressed")
	}
	proxy.commentary.mu.Lock()
	proxy.commentary.routes[token].expires = time.Now().Add(-time.Second)
	proxy.commentary.mu.Unlock()
	if proxy.commentary.subscribeThread("remapped", "stable-thread", "") == "" {
		t.Fatal("thread refresh rejected")
	}
	request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{message})}}
	if err := proxy.reconcileInputPrefix(request, "remapped"); err != nil {
		t.Fatal(err)
	}
	if string(request.fields["input"]) != "[]" {
		t.Fatalf("remapped replay leaked commentary: %s", request.fields["input"])
	}
	if len(proxy.commentaryMessageIDs("other-thread-session")) != 0 {
		t.Fatal("provenance crossed threads")
	}
}

func TestThreadCommentaryCannotReclaimToolHistoryCapacity(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	// Four almost-full sessions exercise both the session and global budgets.
	script := strings.Repeat("x", maxMekugiHistorySessionBytes-256)
	for i := range 4 {
		session := fmt.Sprint(i)
		if err := proxy.rememberBatch(session, map[string]mekugiHistory{"essential": {script: script}}); err != nil {
			t.Fatal(err)
		}
	}
	before := proxy.historyBytes
	token := proxy.commentary.subscribeThread("0", "thread", "")
	transform := &mekugiResponseTransform{proxy: proxy, historySessionID: "0", shellThreadID: "thread"}
	for range maxCommentaryEventsPerRoute {
		proxy.commentary.publish(token, "auxiliary", false)
		publication := proxy.commentary.drainSession("0", "thread")[0]
		if transform.runtimeCommentaryMessage(publication) == nil {
			t.Fatal("independent commentary capacity unavailable")
		}
	}
	if proxy.historyBytes != before {
		t.Fatal("commentary consumed essential history budget")
	}
	for i := range 4 {
		if _, exists := proxy.history(fmt.Sprint(i), "essential"); !exists {
			t.Fatal("commentary evicted essential history")
		}
	}
	if err := proxy.rememberBatch("0", map[string]mekugiHistory{"later": {script: "ok"}}); err != nil {
		t.Fatalf("later tool admission blocked: %v", err)
	}
	if _, exists := proxy.history("0", "essential"); !exists {
		t.Fatal("commentary impaired later tool admission")
	}
}

func TestThreadCommentaryProvenanceCapacitySuppressesOnlyCommentary(t *testing.T) {
	b := newCommentaryBroker()
	token := b.subscribeThread("session", "thread", "")
	for range maxThreadCommentaryIDs {
		b.publish(token, "bounded", false)
		if len(b.drain(token)) != 1 {
			t.Fatal("provenance capacity rejected early")
		}
	}
	b.publish(token, "overflow", false)
	if len(b.drain(token)) != 0 || b.threadIDCount != maxThreadCommentaryIDs {
		t.Fatal("provenance capacity unbounded")
	}
	if len(b.threadMessageIDs("session")) != maxThreadCommentaryIDs {
		t.Fatal("capacity exhaustion discarded replay provenance")
	}
}

func TestChildThreadCommentaryPreservesSubstantiveStreamResult(t *testing.T) {
	transform, proxy := newRuntimeCommentaryTransform(t)
	transform.subagentTurn = true
	answer := map[string]any{"type": "message", "id": "answer", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Final answer."}}}
	if events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer})); err != nil || len(events) != 0 {
		t.Fatalf("answer delivery = %s, %v", events, err)
	}
	token := proxy.commentary.subscribeThread(transform.historySessionID, transform.shellThreadID, "")
	proxy.commentary.publish(token, "child progress", false)
	events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{answer}}}))
	if err != nil || len(events) != 2 {
		t.Fatalf("terminal emitted standalone commentary: %s, %v", events, err)
	}
	if !bytes.Contains(events[0], []byte(`"text":"Final answer."`)) {
		t.Fatalf("child provider answer missing: %s", events[0])
	}

	var terminal struct {
		Type     string `json:"type"`
		Response struct {
			Output []map[string]json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(events[1], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "response.completed" || len(terminal.Response.Output) != 2 || !bytes.Contains(terminal.Response.Output[1]["content"], []byte("Final answer.")) || !bytes.Contains(terminal.Response.Output[0]["content"], []byte("child progress")) {
		t.Fatalf("child terminal order = %s", events[1])
	}
}

func TestThreadCommentaryDoesNotCrossSharedRoutingSession(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	workspace := t.TempDir()
	prepare := func(thread string) *mekugiResponseTransform {
		t.Helper()
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
			"model": "gpt-test",
			"input": []any{testCodeModeAdditionalTools(testCodeModeDescription)},
			"tools": []any{map[string]any{"type": "function", "name": "lookup"}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		transform, err := proxy.prepareRequest(t.Context(), &request, "shared-session", thread,
			codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(transform.Close)
		return transform
	}
	for _, timing := range []string{"deferred", "terminal"} {
		t.Run(timing, func(t *testing.T) {
			root := prepare("root-thread")
			root.Close()
			token := proxy.commentary.subscribeThread(root.historySessionID, "root-thread", "")
			if timing == "deferred" {
				proxy.commentary.publish(token, "root shell progress", false)
			}
			child := prepare("child-thread")
			if timing == "terminal" {
				proxy.commentary.publish(token, "root shell progress", false)
			}
			answer := map[string]any{"type": "message", "id": "child-answer", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Child result."}}}
			events, err := child.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{answer}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(bytes.Join(events, nil), []byte("root shell progress")) {
				t.Fatal("another thread consumed root shell commentary through the shared routing session")
			}
			next := prepare("root-thread")
			events, err = next.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.created"}))
			if err != nil || !bytes.Contains(bytes.Join(events, nil), []byte("root shell progress")) {
				t.Fatal("root shell commentary was not delivered to its originating thread")
			}
			// The other request remains active while the root reaches a terminal.
			proxy.commentary.publish(token, "later root progress", false)
			events, err = next.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}},
			}))
			if err != nil || !bytes.Contains(bytes.Join(events, nil), []byte("later root progress")) {
				t.Fatal("concurrent root terminal did not deliver its shell commentary", err)
			}
			child.Close()
			next.Close()
		})
	}
}

func TestThreadCommentaryDeferredDeliverySurvivesRemap(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			token := proxy.commentary.subscribeThread(transform.historySessionID, transform.shellThreadID, "")
			proxy.commentary.publish(token, "queued before remap", false)
			transform.deferredCommentary = proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID)
			if len(transform.deferredCommentary) != 1 {
				t.Fatal("request did not claim its queued publication")
			}
			publication := transform.deferredCommentary[0]
			proxy.commentary.subscribeThread("remapped-session", transform.shellThreadID, "")
			var visible []byte
			if stream {
				events, err := transform.TransformSSE([]byte(`{"type":"response.created"}`))
				if err != nil {
					t.Fatal(err)
				}
				visible = bytes.Join(events, nil)
			} else {
				var err error
				visible, err = transform.TransformJSON([]byte(`{"status":"completed","output":[]}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Contains(visible, []byte("queued before remap")) {
				t.Fatalf("claimed publication lost after remap: %s", visible)
			}
			if events := proxy.drainCommentarySession("remapped-session", transform.shellThreadID); len(events) != 0 {
				t.Fatal("remapping duplicated an already claimed publication")
			}
			other := &mekugiResponseTransform{proxy: proxy, historySessionID: "remapped-session", shellThreadID: "other-thread"}
			if other.runtimeCommentaryMessage(publication) != nil {
				t.Fatal("another thread rendered the claimed publication")
			}
			replay := &parsedResponsesRequest{fields: map[string]json.RawMessage{
				"input": mustMarshalJSON([]any{assistantCommentaryMessage(publication.messageID, publication.text)}),
			}}
			if err := proxy.reconcileInputPrefix(replay, "remapped-session"); err != nil || string(replay.fields["input"]) != "[]" {
				t.Fatalf("remapped replay leaked commentary: %s, %v", replay.fields["input"], err)
			}
		})
	}
}
