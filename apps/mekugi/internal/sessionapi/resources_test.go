package sessionapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

type blockedResourceWriter struct {
	*httptest.ResponseRecorder
	ctx         context.Context
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func (w *blockedResourceWriter) Write(data []byte) (int, error) {
	w.startedOnce.Do(func() { close(w.started) })
	select {
	case <-w.release:
		return w.ResponseRecorder.Write(data)
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func TestResourceTransfersAreIndependentAndBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub, server := NewHub(t.Context(), nil), New()
		registerTestSession(hub, "thread", server, Summary{Thread: "thread", Workspace: "/workspace"})
		text := strings.Repeat("retained ", 128<<10)
		if err := server.Publish(Frame{Session: Session{Thread: "thread"}, Main: &activity.Publication{Entries: []activity.Entry{{Seq: 1, Kind: "text", Text: text, Revision: 1}}}}); err != nil {
			t.Fatal(err)
		}
		handler := hub.Handler()
		ref := ContentReference{Attachment: server.readAttachment, Kind: "main", Seq: 1, Revision: server.main.entries[1].revision}
		params := string(testJSON(t, RPCParams{Thread: "thread", Body: testJSON(t, ref)}))
		conn := pipeControl(t, hub)
		controlInitialize(t, conn)
		for _, cancelRead := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			writer := &blockedResourceWriter{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, started: make(chan struct{}), release: make(chan struct{})}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if failure := recover(); failure != nil && failure != http.ErrAbortHandler {
						panic(failure)
					}
				}()
				handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, ResourcePath+"content", strings.NewReader(params)).WithContext(ctx))
			}()
			<-writer.started
			if len(hub.preparations) != 1 {
				t.Fatal("blocked response lost capacity ownership")
			}
			other := httptest.NewRecorder()
			handler.ServeHTTP(other, httptest.NewRequest(http.MethodPost, ResourcePath+"content", strings.NewReader(params)))
			if other.Code != 200 || !strings.Contains(other.Body.String(), text) {
				t.Fatal("concurrent resource read blocked or lost content")
			}
			controlWrite(t, conn, `{"jsonrpc":"2.0","id":"read","method":"session/read","params":{"thread":"thread"}}`)
			if reply := controlRead(t, conn); reply.Error != nil {
				t.Fatal("resource write blocked control")
			}
			for range preparationLimit - 1 {
				hub.preparations <- struct{}{}
			}
			full := httptest.NewRecorder()
			handler.ServeHTTP(full, httptest.NewRequest(http.MethodPost, ResourcePath+"content", strings.NewReader(params)))
			if full.Code != 503 {
				t.Fatal("resource reads bypassed aggregate preparation budget")
			}
			for range preparationLimit - 1 {
				<-hub.preparations
			}
			if cancelRead {
				cancel()
			} else {
				close(writer.release)
			}
			<-done
			cancel()
			if len(hub.preparations) != 0 {
				t.Fatal("completed resource retained capacity")
			}
		}
		for _, method := range []string{"session/content", "session/image", "session/attachment-image"} {
			controlWrite(t, conn, `{"jsonrpc":"2.0","id":"removed","method":"`+method+`","params":{}}`)
			if reply := controlRead(t, conn); reply.Error == nil || reply.Error.Code != -32601 {
				t.Fatal("control RPC still admits resource bytes")
			}
		}
	})
}

func TestResourceRequestLimitsRetainSemanticStatus(t *testing.T) {
	hub := NewHub(t.Context(), nil)
	defer hub.Close()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"body":{"padding":"` + strings.Repeat("x", 1024) + `"}}`, 413},
		{`{"thread":"` + strings.Repeat("x", 128<<10) + `","body":{}}`, 413},
		{`{"body":`, 400},
		{`{"unknown":true,"body":{}}`, 400},
	} {
		response := httptest.NewRecorder()
		hub.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, ResourcePath+"content", strings.NewReader(tc.body)))
		if response.Code != tc.status {
			t.Fatalf("request status %d, want %d: %s", response.Code, tc.status, response.Body)
		}
	}
}
