package router

import (
	"bytes"
	jsonv1 "encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestAppServerBTWCompactionRejectsBeforeProvider(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	workspace := t.TempDir()
	thread := "side"
	proxy.setBTWThread(thread, true)
	requestFor := func(thread string) (codexresponses.Request, http.Header) {
		request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
			"model": "gpt-test", "stream": true,
			"tool_choice": "auto", "parallel_tool_calls": false,
			"input": []any{map[string]any{"role": "user", "content": "Summarize."}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		metadata, ok := decodeCodexTurnMetadata(serverCompactionMetadataHeaders(t))
		if !ok {
			t.Fatal("invalid fixture metadata")
		}
		metadata.ThreadID = thread
		metadata.Directories = map[string]jsonv1.RawMessage{workspace: nil}
		headers := make(http.Header)
		headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
		headers.Set(codexresponses.ThreadIDHeader, thread)
		return request, headers
	}
	request, headers := requestFor(thread)
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	err := executeRequest(t.Context(), t.Context(), request, headers, "side", provider, &output, nil, proxy)
	compatibility, ok := errors.AsType[*requestCompatibilityError](err)
	if !ok || compatibility.code != "btw_compaction_required" || compatibility.message != btwCompactionRequired {
		t.Fatalf("rejection = %v", err)
	}
	if len(provider.forwarded) != 0 || output.Len() != 0 {
		t.Fatalf("side request escaped: upstream=%d output=%q", len(provider.forwarded), output.String())
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.Fields)))
	httpRequest.Header = headers
	recorder := httptest.NewRecorder()
	responsesHandler(t.Context(), time.Second, provider, nil, proxy)(recorder, httpRequest)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), btwCompactionRequired) {
		t.Fatalf("HTTP rejection: %d %s", recorder.Code, recorder.Body.String())
	}
	// Main compaction still goes to the provider while a side thread is registered.
	mainRequest, mainHeaders := requestFor("main")
	provider.results = []serverForwardResult{{response: serverHTTPResponse(`{"id":"main-compact","status":"completed","output":[]}`)}}
	if err := executeRequest(t.Context(), t.Context(), mainRequest, mainHeaders, "main", provider, io.Discard, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("Main compaction requests=%d want=1", len(provider.forwarded))
	}
}

func TestAppServerBTWCompactionRestoresAttachmentsOnce(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "error-first", true: "completion-first"}[completionFirst], func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.proxy = newManagedMekugiProxy(t)
			u.Turn, u.status = "main-turn", "Working"
			appServerTestKeys(t, u, "/btw explain ")
			image := filepath.Join(t.TempDir(), "side.png")
			u.attachImage(image)
			appServerTestKeys(t, u, "\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
			if !u.proxy.isBTWThread("side") {
				t.Fatal("side not registered before turn/start")
			}
			start := btwTestRequest(t, w, "turn/start", "side")
			btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
			if len(u.btw.pending.images) != 1 || u.btw.pending.text == "" {
				t.Fatal("start acknowledgement discarded retry input")
			}
			appServerTestKeys(t, u, "newer draft")
			errorEvent := func() {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": "side", "error": map[string]any{"message": "HTTP 400: " + btwCompactionRequired}})
			}
			completion := func() {
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed", "error": map[string]any{"message": btwCompactionRequired}}})
			}
			if completionFirst {
				completion()
				errorEvent()
			} else {
				errorEvent()
				completion()
			}
			before := u.draft
			errorEvent()
			completion()
			if u.draft != before || strings.Count(before, "/btw") != 1 || !strings.HasSuffix(before, "newer draft") || len(u.images) != 1 || u.images[0].path != image {
				t.Fatalf("restoration lost/duplicated input: %q images=%+v", u.draft, u.images)
			}
			if u.btw.busy || u.btw.pending.text != "" || u.btw.status != btwCompactionRequired || u.Turn != "main-turn" || u.status != "Working" {
				t.Fatalf("failure state = %+v Main=%s/%s", u.btw, u.Turn, u.status)
			}
			if err := u.submitBTW(); err != nil {
				t.Fatal(err)
			}
			if w.Len() != 0 || u.draft != before {
				t.Fatal("rejected side accepted follow-up")
			}
		})
	}
}

func TestAppServerBTWCompactionCloseRecreateIgnoresLateFailure(t *testing.T) {
	u, w := newAppServerTestUI()
	u.proxy = newManagedMekugiProxy(t)
	btwTestStart(t, u, w)
	old := u.btw
	if err := u.closeBTW(); err != nil {
		t.Fatal(err)
	}
	interrupt := btwTestRequest(t, w, "turn/interrupt", "side")
	appServerTestKeys(t, u, "/btw fresh\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"new-side"}}`)
	start := btwTestRequest(t, w, "turn/start", "new-side")
	btwTestReply(t, u, start, `{"turn":{"id":"new-turn"}}`)
	appServerTestNotify(t, u, "error", map[string]any{"threadId": "side", "error": map[string]any{"message": btwCompactionRequired}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed"}})
	btwTestReply(t, u, interrupt, `{}`)
	unload := btwTestRequest(t, w, "thread/unsubscribe", "side")
	btwTestReply(t, u, unload, `{}`)
	if u.btw == old || u.btw.thread != "new-side" || !u.btw.busy || u.draft != "" || u.proxy.isBTWThread("side") || !u.proxy.isBTWThread("new-side") {
		t.Fatalf("late events crossed lifecycle: %+v draft=%q", u.btw, u.draft)
	}
}

func TestAppServerBTWCompactionBeforeStartAckDoesNotResend(t *testing.T) {
	u, w := newAppServerTestUI()
	u.proxy = newManagedMekugiProxy(t)
	appServerTestKeys(t, u, "/btw initial\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	start := btwTestRequest(t, w, "turn/start", "side")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "failed", "error": map[string]any{"message": btwCompactionRequired}}})
	before := u.draft
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	if w.Len() != 0 || u.btw.turn != "" || u.btw.busy || u.draft != before || !strings.Contains(before, "initial") {
		t.Fatalf("late start acknowledgement revived rejection: panel=%+v draft=%q sent=%q", u.btw, u.draft, w.String())
	}
}

func TestUISnapshotBTWCompactionRequired(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = diffrender.DarkTheme
	u.btw = &appServerBTW{question: "Why did this fail?", status: btwCompactionRequired, alert: true}
	for _, test := range []struct {
		name  string
		width int
	}{{"btw-compaction-required", 110}, {"btw-compaction-required-narrow", 60}} {
		t.Run(test.name, func(t *testing.T) {
			uisnapshot.Assert(t, "testdata/snapshots/"+test.name+".txt", strings.Join(u.btwRows(test.width, 8), "\n")+"\n")
		})
	}
}
