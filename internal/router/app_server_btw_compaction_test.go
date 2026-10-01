package router

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerBTWCompactionRejectsBeforeSynthesisOrProvider(t *testing.T) {
	for _, mode := range []string{"off", "auto", "slice"} {
		t.Run(mode, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = mode
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Retained task"), State: new("working")}}); err != nil {
				t.Fatal(err)
			}
			proxy.setBTWThread(thread, true)
			request, headers := journalCompactionRequest(t, workspace, thread)
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
			if _, err := os.Stat(filepath.Join(proxy.replayStore.directory, journalCompactionName(workspace, thread))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("side synthesis persisted: %v", err)
			}
			wire := mustTestJSON(t, request.fields)
			httpRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(wire))
			httpRequest.Header = headers
			recorder := httptest.NewRecorder()
			responsesHandler(t.Context(), time.Second, provider, nil, proxy)(recorder, httpRequest)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), btwCompactionRequired) {
				t.Fatalf("HTTP rejection: %d %s", recorder.Code, recorder.Body.String())
			}
			// A separate Main thread follows normal policy even while a side is registered.
			mainRequest, mainHeaders := journalCompactionRequest(t, workspace, "main")
			mainWire, err := journalCompactionSSE("main-compact", "gpt-test", "Main summary")
			if err != nil {
				t.Fatal(err)
			}
			provider.results = []serverForwardResult{{response: serverHTTPResponse(string(mainWire))}}
			provider.results[0].response.Header.Set("Content-Type", "text/event-stream")
			if err := executeRequest(t.Context(), t.Context(), mainRequest, mainHeaders, "main", provider, io.Discard, nil, proxy); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 {
				t.Fatalf("Main compaction blocked: requests=%d", len(provider.forwarded))
			}
		})
	}
}

func TestAppServerBTWCompactionRestoresAttachmentsOnce(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "error-first", true: "completion-first"}[completionFirst], func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.proxy = newManagedMekugiProxy(t)
			u.turn, u.status = "main-turn", "Working"
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
			if u.btw.busy || u.btw.pending.text != "" || u.btw.status != btwCompactionRequired || u.turn != "main-turn" || u.status != "Working" {
				t.Fatalf("failure state = %+v Main=%s/%s", u.btw, u.turn, u.status)
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
	u.view.painter.Theme = livediff.DarkTheme
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
