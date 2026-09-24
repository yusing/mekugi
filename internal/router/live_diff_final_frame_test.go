package router

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func newLiveDiffFinalFrameTransform(t *testing.T, native bool) (*mekugiResponseTransform, *liveDiffBroker, *liveDiffSubscriber) {
	t.Helper()
	proxy := newManagedMekugiProxy(t)
	var transform *mekugiResponseTransform
	if native {
		transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
	} else {
		transform, _, _, _ = newMekugiTestTransformWithProxy(t, proxy)
	}
	workspace, thread := transform.directory, transform.threadID
	broker := newLiveDiffBroker(t.Context())
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {thread: true}}}
	broker.setScope(scope)
	sub := broker.subscribe()
	<-sub.events // retained scope
	proxy.autoLiveDiff = &autoLiveDiff{events: broker, requested: true, scope: scope}
	proxy.autoLiveDiff.enabled.Store(true)
	return transform, broker, sub
}

func requireLiveDiffSSEUnchanged(t *testing.T, transform *mekugiResponseTransform, payload []byte) {
	t.Helper()
	visible, err := transform.TransformSSE(payload)
	if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], payload) {
		t.Fatalf("stock SSE event changed: %q, %v", visible, err)
	}
}

func TestLiveDiffFinalFrameCustomInputDoneFlushesAuthoritativeJavaScript(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	workerCtx, cancel := context.WithCancel(transform.ctx)
	transform.ctx = workerCtx
	defer cancel()
	delta := `text(await tools.write_stdin({session_id:24751,chars:"",yield_time_ms:1000,max_output_tokens:1000}));` + "\n" +
		`const result = await tools.write_stdin({session_id:48,chars:"",yield_time_ms:1000,max_output_tokens:`
	fullInput := delta + `100}); text(JSON.stringify(result));`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "exec-item", "call_id": "exec-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "exec-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.HasSuffix(preview.Input, "max_output_tokens:") && !preview.Complete
	})
	if partial.Input != delta {
		t.Fatalf("partial JavaScript preview = %q, want delta %q", partial.Input, delta)
	}
	worker := transform.previews["exec-item"]
	if worker == nil {
		t.Fatal("Code Mode stream has no preview worker")
	}

	// Queue one more partial delta immediately before done, without waiting for
	// its debounce window. Done still contains bytes absent from all deltas.
	requireLiveDiffSSEUnchanged(t, transform, mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "exec-item", "delta": "100}); text(JSON",
	}))
	finalEvent := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "exec-item", "call_id": "exec-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, finalEvent)
	// Completion must own an independent final pass, even when request teardown
	// cancels the stream context before the coalesced preview timer fires.
	cancel()
	transform.Close()

	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && preview.Input == fullInput
	})
	if complete.Status != "STREAMING SCRIPT" || len(complete.Syntax) != 1 || complete.Syntax[0].Path != "preview.js" {
		t.Fatalf("final JavaScript preview = %+v", complete)
	}
	select {
	case <-worker.done:
	case <-time.After(5 * time.Second):
		t.Fatal("final preview worker did not finish")
	}

	// A late delta or an already-running progress pass must not replace the
	// completed snapshot with a live, truncated version.
	worker.appendDelta(" late update")
	broker.mu.Lock()
	_, active := broker.previews[complete.ID]
	broker.mu.Unlock()
	if active {
		t.Fatal("completed preview was resurrected as a live preview")
	}
	select {
	case <-sub.previewReady:
		for _, event := range broker.takePreviews(sub) {
			if event.Preview != nil && event.Preview.ID == complete.ID && !event.Preview.Complete {
				t.Fatalf("late progress replaced completion: %+v", event.Preview)
			}
		}
	default:
	}
}

func TestLiveDiffFinalFrameOutputItemDoneFallback(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	delta := `const result = await tools.write_stdin({session_id:48,chars:"",yield_time_ms:1000,`
	fullInput := delta + `max_output_tokens:100}); text(JSON.stringify(result));`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "fallback-item", "call_id": "fallback-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "fallback-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "session_id:48") && !preview.Complete
	})
	completedItem := map[string]any{"type": "custom_tool_call", "id": "fallback-item", "call_id": "fallback-call", "name": "exec", "input": fullInput, "status": "completed"}
	event := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": completedItem})
	requireLiveDiffSSEUnchanged(t, transform, event)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && preview.Input == fullInput
	})
	if len(complete.Syntax) != 1 || complete.Syntax[0].Path != "preview.js" {
		t.Fatalf("fallback completion was not rendered as JavaScript: %+v", complete)
	}
}

func TestLiveDiffFinalFrameNativeExecArgumentsDoneUsesFullCommand(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, true)
	delta := `{"cmd":"printf 'first `
	arguments := `{"cmd":"printf 'first FINAL_NATIVE'"}`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "native-item", "call_id": "native-call", "name": "exec_command", "arguments": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "native-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	event := mustTestJSON(t, map[string]any{"type": "response.function_call_arguments.done", "item_id": "native-item", "arguments": arguments})
	requireLiveDiffSSEUnchanged(t, transform, event)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && strings.Contains(preview.Input, "FINAL_NATIVE")
	})
	if strings.Contains(complete.Input, `"cmd"`) || len(complete.Syntax) != 1 || complete.Syntax[0].Path != "stream.sh" {
		t.Fatalf("native exec completion did not project the full command: %+v", complete)
	}
}

func TestLiveDiffCancellationBeforeDoneDiscardsActivePreview(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	requestCtx, cancel := context.WithCancel(transform.ctx)
	transform.ctx = requestCtx
	delta := `const activeStream = true;`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "cancel-item", "call_id": "cancel-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "cancel-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Input == delta && !preview.Complete
	})
	if !liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("streaming preview was not active before cancellation")
	}
	worker := transform.previews["cancel-item"]
	if worker == nil {
		t.Fatal("Code Mode stream has no preview worker")
	}

	// The response stream can disappear without ever sending input.done. Allow
	// the preview worker to exit before transform teardown does its cleanup.
	cancel()
	waitLiveDiffWorkerDone(t, worker)
	transform.Close()
	if liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("canceled preview remained in the broker's active set after transform close")
	}
}

func TestLiveDiffInputOverflowAfterPartialPreviewDiscardsActiveState(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	delta := `const activeStream = true;`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "overflow-item", "call_id": "overflow-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "overflow-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Input == delta && !preview.Complete
	})
	if !liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("streaming preview was not active before input overflow")
	}
	worker := transform.previews["overflow-item"]
	if worker == nil {
		t.Fatal("Code Mode stream has no preview worker")
	}
	worker.appendDelta(strings.Repeat("x", (256<<10)+1))
	waitLiveDiffWorkerDone(t, worker)
	worker.stop()
	transform.Close()
	if liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("overflowed preview remained in the broker's active set after worker exit")
	}
}

func TestLiveDiffFinalFrameNativeHeredocIncludesLastLineAfterContextCancellation(t *testing.T) {
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, true)
	requestCtx, cancel := context.WithCancel(transform.ctx)
	defer cancel()
	transform.ctx = requestCtx
	target := filepath.Join(transform.directory, "native-final.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "cat >native-final.txt <<'END'\nfirst\nFINAL_NATIVE_EDIT\nEND\n"
	argumentsBytes, err := json.Marshal(map[string]string{"cmd": command})
	if err != nil {
		t.Fatal(err)
	}
	arguments := string(argumentsBytes)
	delta := arguments[:strings.Index(arguments, "FINAL_NATIVE_EDIT")]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "native-final-item", "call_id": "native-final-call", "name": "exec_command", "arguments": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "native-final-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	event := mustTestJSON(t, map[string]any{"type": "response.function_call_arguments.done", "item_id": "native-final-item", "arguments": arguments})
	requireLiveDiffSSEUnchanged(t, transform, event)
	cancel()

	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && len(preview.Files) == 1 && strings.Contains(preview.Files[0].UnifiedDiff(), "FINAL_NATIVE_EDIT")
	})
	if !strings.Contains(complete.Files[0].UnifiedDiff(), "+FINAL_NATIVE_EDIT") {
		t.Fatalf("native final diff lost the last heredoc line: %+v", complete.Files[0])
	}
}

func TestLiveDiffFinalFrameCodeModeHeredocIncludesLastLineAfterContextCancellation(t *testing.T) {
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	requestCtx, cancel := context.WithCancel(transform.ctx)
	defer cancel()
	transform.ctx = requestCtx
	target := filepath.Join(transform.directory, "code-mode-final.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "cat >code-mode-final.txt <<'END'\nfirst\nFINAL_CODEMODE_EDIT\nEND\n"
	encodedCommand, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	fullInput := "const result = await tools.exec_command({cmd:" + string(encodedCommand) + "}); text(JSON.stringify(result));"
	before, _, ok := strings.Cut(fullInput, "FINAL_CODEMODE_EDIT")
	if !ok {
		t.Fatal("fixture command did not contain its final marker")
	}
	delta := before
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "code-mode-final-item", "call_id": "code-mode-final-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "code-mode-final-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	if transform.previews["code-mode-final-item"] == nil {
		t.Fatal("Code Mode heredoc stream has no preview worker")
	}
	event := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "code-mode-final-item", "call_id": "code-mode-final-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, event)
	cancel()

	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && len(preview.Files) == 1 && strings.Contains(preview.Files[0].UnifiedDiff(), "FINAL_CODEMODE_EDIT")
	})
	if !strings.Contains(complete.Files[0].UnifiedDiff(), "+FINAL_CODEMODE_EDIT") {
		t.Fatalf("Code Mode final diff lost the last heredoc line: %+v", complete.Files[0])
	}
}

func liveDiffBrokerHasActivePreview(broker *liveDiffBroker, id string) bool {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	_, active := broker.previews[id]
	return active
}

func waitLiveDiffWorkerDone(t *testing.T, worker *liveDiffPreviewWorker) {
	t.Helper()
	select {
	case <-worker.done:
	case <-time.After(5 * time.Second):
		t.Fatal("live diff preview worker did not stop")
	}
}

func TestLiveDiffFinalFramePTYShowsCompleteJavaScriptAfterTruncatedPreview(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	sub := broker.subscribe()
	<-sub.events
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 18)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })

	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)
	partial := `const output = await tools.write_stdin({session_id:48,chars:"",yield_time_ms:1000` + "\n"
	fullInput := partial + `,max_output_tokens:100}); text(JSON.stringify(output)); // FINAL_FRAME_MARKER`
	worker.appendDelta(partial)
	firstFrame := ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "session_id:48")
	})
	if strings.Contains(ansi.Strip(firstFrame), "FINAL_FRAME_MARKER") {
		t.Fatal("truncated streaming frame unexpectedly contained the final marker")
	}
	worker.finish(fullInput)
	finalFrame := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(frame, "STREAMING COMPLETE") && strings.Contains(plain, "FINAL_FRAME_MARKER")
	})
	plain := ansi.Strip(finalFrame)
	for _, want := range []string{"STREAMING COMPLETE", "session_id:48", "FINAL_FRAME_MARKER"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("final PTY frame missing %q: %q", want, plain)
		}
	}
	select {
	case <-worker.done:
	case <-time.After(5 * time.Second):
		t.Fatal("final PTY preview worker did not finish")
	}
	worker.appendDelta(" // LATE_RESURRECTION")
	broker.mu.Lock()
	_, active := broker.previews[worker.preview.ID]
	broker.mu.Unlock()
	if active {
		t.Fatal("completed PTY preview was resurrected")
	}
}

func TestLiveDiffFinalFrameTransformCloseDoesNotDiscardFinalUpdate(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t, false)
	delta := `text(await tools.write_stdin({session_id:48,chars:""`
	fullInput := delta + `,yield_time_ms:1000,max_output_tokens:100}));`
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "close-item", "call_id": "close-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "close-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "session_id:48") && !preview.Complete
	})
	worker := transform.previews["close-item"]
	if worker == nil {
		t.Fatal("Code Mode stream has no preview worker")
	}
	event := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "close-item", "call_id": "close-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, event)
	transform.Close()
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && preview.Input == fullInput
	})
	if complete.Status != "STREAMING SCRIPT" {
		t.Fatalf("completion status = %q, want STREAMING SCRIPT with Complete=true", complete.Status)
	}
}
