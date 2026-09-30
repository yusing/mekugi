package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCriticalNoticeNeverEntersProviderResponses(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			issues := NewCriticalErrors()
			queueCritical(issues, "session")
			noticeID := issues.entries[0].id
			provider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(stream)}}}
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			var visible bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, nil, "session", provider, &visible, issues, nil); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(visible.Bytes(), []byte(noticeID)) || bytes.Contains(visible.Bytes(), []byte("Enable supported tools.")) {
				t.Fatalf("native notice leaked into provider response: %s", visible.Bytes())
			}
			if len(issues.Pending()) != 1 {
				t.Fatal("request completion acknowledged a notice before native paint")
			}
		})
	}
}

func TestPreviouslyGeneratedNoticeReplayUsesDurableExactProvenance(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const legacyID = "router-owned-legacy-notice"
	if err := store.putCommentary(t.Context(), workspace, []string{legacyID}); err != nil {
		t.Fatal(err)
	}
	fresh := newManagedMekugiProxy(t)
	fresh.replayStore = store
	replay := serverRequest(t, func(fields map[string]any) {
		input := fields["input"].([]any)
		fields["input"] = append([]any{
			assistantCommentaryMessage(legacyID, "old router notice"),
			assistantCommentaryMessage("model-owned", "model-authored message"),
		}, input...)
	})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: criticalTestResponse(false)}}}
	if err := executeRequest(t.Context(), t.Context(), replay,
		serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}),
		"fork-session", provider, io.Discard, NewCriticalErrors(), fresh); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(provider.forwarded[0], []byte(legacyID)) || !bytes.Contains(provider.forwarded[0], []byte("model-owned")) {
		t.Fatalf("replay did not remove only retained router provenance: %s", provider.forwarded[0])
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
