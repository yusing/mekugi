package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCriticalNoticeReplayUsesDurableExactProvenance(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			issues := NewCriticalErrors()
			queueCritical(issues, "parent-session")
			noticeID := issues.entries[0].id

			first := newManagedMekugiProxy(t)
			first.replayStore = store
			provider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(stream)}}}
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			var visible bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request,
				serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
				"parent-session", provider, &visible, issues, first, nil); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(visible.Bytes(), []byte(noticeID)) {
				t.Fatalf("critical notice was not emitted: %s", visible.Bytes())
			}

			// A fresh proxy and changed routing session model resume/fork replay.
			fresh := newManagedMekugiProxy(t)
			fresh.replayStore = store
			replayProvider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(stream)}}}
			replay := serverRequest(t, func(fields map[string]any) {
				fields["stream"] = stream
				input := fields["input"].([]any)
				fields["input"] = append([]any{assistantCommentaryMessage(noticeID, "router notice")}, input...)
			})
			if err := executeRequest(t.Context(), t.Context(), replay,
				serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
				"fork-session", replayProvider, io.Discard, NewCriticalErrors(), fresh, nil); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(replayProvider.forwarded[0], []byte(noticeID)) {
				t.Fatalf("durably retained notice reached provider: %s", replayProvider.forwarded[0])
			}
		})
	}
}

func TestCriticalNoticeRetentionFailureSuppressesWithoutAcknowledging(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.maxCommentaryBytes = 0
	issues := NewCriticalErrors()
	queueCritical(issues, "session")
	transform := issues.transform("session", false)
	transform.retain(t.Context(), store, t.TempDir())
	body := []byte(`{"status":"completed","output":[{"type":"message","id":"answer","content":[]}]}`)
	visible, err := transform.TransformJSON(body)
	if err != nil || !bytes.Equal(visible, body) {
		t.Fatalf("retention failure changed substantive output: %s, %v", visible, err)
	}
	transform.finish(true)
	if len(issues.Pending()) != 1 {
		t.Fatal("suppressed notice was acknowledged")
	}
}

func TestCriticalNoticePassthroughStillEmitsWithoutReplayStore(t *testing.T) {
	issues := NewCriticalErrors()
	queueCritical(issues, "session")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(false)}}}
	var visible bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), nil, "session",
		provider, &visible, issues, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(visible.Bytes(), []byte(issues.entries[0].id)) {
		t.Fatalf("passthrough suppressed queued notice: %s", visible.Bytes())
	}
}

func TestCompactionCriticalNoticeRetainsReplayProvenanceWithoutChangingRequest(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issues := NewCriticalErrors()
	queueCritical(issues, "compaction-session")
	noticeID := issues.entries[0].id
	parsed, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{}},
			map[string]any{"type": "message", "role": "user", "content": "compact exactly"},
		},
		"tool_choice": "auto", "parallel_tool_calls": false, "stream": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(parsed.fields)
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, map[string]any{
		"request_kind": "compaction", "turn_id": "turn-1", "workspaces": map[string]any{workspace: nil},
		"compaction": map[string]any{
			"trigger": "auto", "reason": "context_limit", "implementation": "responses",
			"phase": "standalone_turn", "strategy": "memento",
		},
	})))
	proxy := newManagedMekugiProxy(t)
	proxy.replayStore = store
	provider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(true)}}}
	var visible bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), parsed, headers, "compaction-session",
		provider, &visible, issues, proxy, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(provider.forwarded[0], original) {
		t.Fatalf("compaction request changed:\n got %s\nwant %s", provider.forwarded[0], original)
	}
	if !bytes.Contains(visible.Bytes(), []byte(noticeID)) {
		t.Fatalf("compaction did not emit retained notice: %s", visible.Bytes())
	}

	fresh := newManagedMekugiProxy(t)
	fresh.replayStore = store
	replay := serverRequest(t, func(fields map[string]any) {
		input := fields["input"].([]any)
		fields["input"] = append([]any{assistantCommentaryMessage(noticeID, "router notice")}, input...)
	})
	replayProvider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(false)}}}
	if err := executeRequest(t.Context(), t.Context(), replay,
		serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
		"ordinary-session", replayProvider, io.Discard, NewCriticalErrors(), fresh, nil); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(replayProvider.forwarded[0], []byte(noticeID)) {
		t.Fatalf("compaction notice reached provider after restart: %s", replayProvider.forwarded[0])
	}
}

func criticalTestResponse(stream bool) *http.Response {
	if !stream {
		return serverHTTPResponse(`{"id":"response","status":"completed","output":[]}`)
	}
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
