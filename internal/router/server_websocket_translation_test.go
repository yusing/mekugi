package router

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yusing/mekugi/internal/livediff"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The provider cannot send the next fragment (or input.done) until the viewer
// has received and rendered the preceding provisional diff.
func TestResponsesWebSocketLiveDiffStreamsBeforeInputDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		deltas     []string
		input      string
		want       []string
		wantOnDisk string
	}{
		{
			name: "hpatch", deltas: []string{"hpatch file.txt <<'PATCH'\ntype \"old\" \"hel", "lo"},
			input: "hpatch file.txt <<'PATCH'\ntype \"old\" \"hello\"\nPATCH\n", want: []string{"hel", "hello"}, wantOnDisk: "old\n",
		},
		{
			name: "cat truncate", deltas: []string{"cat >file.txt <<'PATCH'\nhel", "lo"},
			input: "cat >file.txt <<'PATCH'\nhello\nPATCH\n", want: []string{"hel", "hello"}, wantOnDisk: "old\n",
		},
		{
			name: "cat append", deltas: []string{"cat >>file.txt <<'PATCH'\nhel", "lo"},
			input: "cat >>file.txt <<'PATCH'\nhello\nPATCH\n", want: []string{"hel", "hello"}, wantOnDisk: "old\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testResponsesWebSocketLiveDiffStreamsBeforeInputDone(t, tc.deltas, tc.input, tc.want, tc.wantOnDisk)
		})
	}
}

func testResponsesWebSocketLiveDiffStreamsBeforeInputDone(t *testing.T, deltas []string, input string, wants []string, wantOnDisk string) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	directory := t.TempDir()
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	proxy.customizedInstructions = true
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	_, broker, _ := liveDiffTestBroker(t, proxy.replayStore, liveDiffScope{Workspaces: map[string]map[string]bool{}})
	proxy.autoLiveDiff = &autoLiveDiff{
		events: broker, requested: true, scope: liveDiffScope{Workspaces: map[string]map[string]bool{}},
		changed: make(chan struct{}, 1),
	}
	proxy.autoLiveDiff.enabled.Store(true)
	events := make(chan liveDiffEvent, 32)
	go liveDiffStream(ctx, broker.descriptor(), events)
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "streaming-diff-session")
	maps.Copy(headers, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{directory: nil}))
	advance := make(chan struct{})
	providerErrors := make(chan error, 1)
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			providerErrors <- err
			return
		}
		defer upstream.CloseNow()
		if _, err := providerSocketRead(ctx, upstream); err != nil {
			providerErrors <- err
			return
		}
		item := map[string]any{"type": "custom_tool_call", "id": "preview-item", "call_id": "preview-call", "name": "shell", "input": "", "status": "in_progress"}
		write := func(value any) bool {
			if err := providerSocketWrite(ctx, upstream, value); err != nil {
				providerErrors <- err
				return false
			}
			return true
		}
		if !write(socketEvent("response.created", "preview-response")) ||
			!write(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}) {
			return
		}
		for _, delta := range deltas {
			if !write(map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "preview-item", "delta": delta}) {
				return
			}
			select {
			case <-advance:
			case <-ctx.Done():
				return
			}
		}
		if !write(map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "preview-item", "input": input}) {
			return
		}
		item["input"], item["status"] = input, "completed"
		if !write(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}) {
			return
		}
		write(map[string]any{"type": "response.completed", "response": map[string]any{"id": "preview-response", "status": "completed", "output": []any{item}}})
		<-ctx.Done()
	}), proxy, headers)
	socketWrite(t, ctx, conn, map[string]any{
		"type": "response.create", "model": "gpt-test", "instructions": "Follow the task.",
		"input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"role": "user", "content": "edit"}},
		"tools": []any{map[string]string{"type": "function", "name": "lookup"}},
	})
	downstream := make(chan struct{})
	go func() {
		defer close(downstream)
		for {
			event, err := providerSocketRead(ctx, conn)
			if err != nil {
				providerErrors <- err
				return
			}
			if jsonString(event, "type") == "error" {
				providerErrors <- fmt.Errorf("router error: %s", mustMarshalJSON(event))
				return
			}
			if jsonString(event, "type") == "response.completed" {
				return
			}
		}
	}()
	nextEvent := func() liveDiffEvent {
		t.Helper()
		select {
		case event, open := <-events:
			if !open {
				t.Fatal("viewer stream closed")
			}
			return event
		case err := <-providerErrors:
			t.Fatalf("provider: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for a live preview")
		}
		return liveDiffEvent{}
	}
	previewID := ""
	for _, want := range wants {
		for {
			event := nextEvent()
			if event.Kind == "change" {
				t.Fatal("published durable change before complete input")
			}
			if event.Preview == nil {
				continue
			}
			if len(event.Preview.Files) != 1 || !strings.Contains(event.Preview.Files[0].Diff, "+"+want) || event.Preview.Input != "" {
				continue
			}
			previewID = event.Preview.ID
			var pane liveDiffPreviewPane
			pane.update(*event.Preview)
			lines, err := pane.render(t.Context(), directory, livediff.DarkTheme, 100, 10)
			if err != nil || !strings.Contains(strings.Join(lines, "\n"), "STREAMING PREVIEW") {
				t.Fatalf("preview renderer: %v, %+v", err, lines)
			}
			if data, _ := os.ReadFile(path); string(data) != wantOnDisk {
				t.Fatal("partial input changed the workspace")
			}
			break
		}
		advance <- struct{}{}
	}
	removed := false
	for !removed {
		event := nextEvent()
		removed = removed || event.Preview != nil && event.Preview.ID == previewID && event.Preview.Workspace == ""
		if event.Kind == "change" {
			t.Fatal("translation published an edit before host execution")
		}
	}
	select {
	case <-downstream:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
}
