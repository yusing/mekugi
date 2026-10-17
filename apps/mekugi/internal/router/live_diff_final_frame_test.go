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

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func newLiveDiffFinalFrameTransform(t *testing.T) (*mekugiResponseTransform, *previewmodel.Session, *reviewSubscription) {
	t.Helper()
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	workspace, thread := transform.directory, transform.threadID
	broker := newLivePreview(t.Context())
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {thread: true}}}
	setLiveScope(broker, scope)
	sub := testReview(broker).Subscribe()
	<-sub.Events() // retained scope

	proxy.review = testReview(broker)
	proxy.review.Preview().Enable()
	return transform, broker, sub
}

func requireLiveDiffSSEUnchanged(t *testing.T, transform *mekugiResponseTransform, payload []byte) {
	t.Helper()
	visible, err := transform.TransformSSE(payload)
	if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], payload) {
		t.Fatalf("stock SSE event changed: %q, %v", visible, err)
	}
}

// liveDiffCodeModeCat is exec input whose literal cat writes lines.
func liveDiffCodeModeCat(t *testing.T, path string, lines ...string) string {
	t.Helper()
	encoded, err := json.Marshal("cat > " + path + " <<'EOF'\n" + strings.Join(lines, "\n") + "\nEOF\n")
	if err != nil {
		t.Fatal(err)
	}
	return "const result = await tools.exec_command({cmd:" + string(encoded) + "}); text(JSON.stringify(result));"
}

func liveDiffPreviewAdds(preview previewmodel.Preview, line string) bool {
	return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+"+line)
}

func TestLiveDiffFinalFrameCustomInputDoneFlushesAuthoritativeEdit(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	workerCtx, cancel := context.WithCancel(transform.ctx)
	transform.ctx = workerCtx
	defer cancel()
	fullInput := liveDiffCodeModeCat(t, "final.txt", "first", "second", "FINAL_DONE_ONLY")
	second := strings.Index(fullInput, "second")
	delta := fullInput[:second]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "exec-item", "call_id": "exec-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "exec-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return liveDiffPreviewAdds(preview, "first") && !preview.Complete
	})
	worker := transform.previews["exec-item"]
	if worker == nil {
		t.Fatal("exec stream has no preview worker")
	}

	// Queue one more partial delta immediately before done, without waiting for
	// its debounce window. Done still contains bytes absent from all deltas.
	requireLiveDiffSSEUnchanged(t, transform, mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "exec-item", "delta": fullInput[second:strings.Index(fullInput, "FINAL")],
	}))
	finalEvent := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "exec-item", "call_id": "exec-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, finalEvent)
	// Completion must own an independent final pass, even when request teardown
	// cancels the stream context before the coalesced preview timer fires.
	cancel()
	transform.Close()

	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete && liveDiffPreviewAdds(preview, "FINAL_DONE_ONLY")
	})
	if complete.Status != previewmodel.PreviewEdit || complete.Input != "" {
		t.Fatalf("final edit preview = %+v", complete)
	}
	select {
	case <-worker.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("final preview worker did not finish")
	}

	// A late delta or an already-running progress pass must not replace the
	// completed snapshot with a live, truncated version.
	worker.Append(" late update")
	_, active := currentLivePreview(broker, complete.ID)
	if active {
		t.Fatal("completed preview was resurrected as a live preview")
	}
	select {
	case <-sub.Ready():
		for _, event := range testReview(broker).TakePreviews(sub) {
			if event.Preview != nil && event.Preview.ID == complete.ID && !event.Preview.Complete {
				t.Fatalf("late progress replaced completion: %+v", event.Preview)
			}
		}
	default:
	}
}

func TestLiveDiffFinalFrameOutputItemDoneFallback(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	fullInput := liveDiffCodeModeCat(t, "fallback.txt", "first", "FALLBACK_FINAL")
	delta := fullInput[:strings.Index(fullInput, "FALLBACK_FINAL")]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "fallback-item", "call_id": "fallback-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "fallback-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return liveDiffPreviewAdds(preview, "first") && !preview.Complete
	})
	completedItem := map[string]any{"type": "custom_tool_call", "id": "fallback-item", "call_id": "fallback-call", "name": "exec", "input": fullInput, "status": "completed"}
	event := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": completedItem})
	requireLiveDiffSSEUnchanged(t, transform, event)
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete && liveDiffPreviewAdds(preview, "FALLBACK_FINAL")
	})
}

func TestLiveDiffCancellationBeforeDoneDiscardsActivePreview(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	requestCtx, cancel := context.WithCancel(transform.ctx)
	transform.ctx = requestCtx
	input := liveDiffCodeModeCat(t, "cancel.txt", "active", "later")
	delta := input[:strings.Index(input, "later")]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "cancel-item", "call_id": "cancel-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "cancel-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return liveDiffPreviewAdds(preview, "active") && !preview.Complete
	})
	if !liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("streaming preview was not active before cancellation")
	}
	worker := transform.previews["cancel-item"]
	if worker == nil {
		t.Fatal("exec stream has no preview worker")
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
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	input := liveDiffCodeModeCat(t, "overflow.txt", "active", "later")
	delta := input[:strings.Index(input, "later")]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "overflow-item", "call_id": "overflow-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "overflow-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return liveDiffPreviewAdds(preview, "active") && !preview.Complete
	})
	if !liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("streaming preview was not active before input overflow")
	}
	worker := transform.previews["overflow-item"]
	if worker == nil {
		t.Fatal("exec stream has no preview worker")
	}
	worker.Append(strings.Repeat("x", (256<<10)+1))
	waitLiveDiffWorkerDone(t, worker)
	worker.Stop()
	transform.Close()
	if liveDiffBrokerHasActivePreview(broker, partial.ID) {
		t.Fatal("overflowed preview remained in the broker's active set after worker exit")
	}
}

func TestLiveDiffFinalFrameCodeModeHeredocIncludesLastLineAfterContextCancellation(t *testing.T) {
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	requestCtx, cancel := context.WithCancel(transform.ctx)
	defer cancel()
	transform.ctx = requestCtx
	target := filepath.Join(transform.directory, "exec-final.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "cat >exec-final.txt <<'END'\nfirst\nFINAL_CODEMODE_EDIT\nEND\n"
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
			"item": map[string]any{"type": "custom_tool_call", "id": "exec-final-item", "call_id": "exec-final-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "exec-final-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	if transform.previews["exec-final-item"] == nil {
		t.Fatal("exec heredoc stream has no preview worker")
	}
	event := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "exec-final-item", "call_id": "exec-final-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, event)
	cancel()

	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete && len(preview.Files) == 1 && strings.Contains(preview.Files[0].UnifiedDiff(), "FINAL_CODEMODE_EDIT")
	})
	if !strings.Contains(complete.Files[0].UnifiedDiff(), "+FINAL_CODEMODE_EDIT") {
		t.Fatalf("exec final diff lost the last heredoc line: %+v", complete.Files[0])
	}
}

func liveDiffBrokerHasActivePreview(broker *previewmodel.Session, id string) bool {
	_, active := currentLivePreview(broker, id)
	return active
}

func waitLiveDiffWorkerDone(t *testing.T, worker *previewbinding.Stream) {
	t.Helper()
	select {
	case <-worker.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("live diff preview worker did not stop")
	}
}

func TestLiveDiffFinalFrameTransformCloseDoesNotDiscardFinalUpdate(t *testing.T) {
	t.Parallel()
	transform, broker, sub := newLiveDiffFinalFrameTransform(t)
	fullInput := liveDiffCodeModeCat(t, "close.txt", "first", "CLOSE_FINAL")
	delta := fullInput[:strings.Index(fullInput, "CLOSE_FINAL")]
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "close-item", "call_id": "close-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "close-item", "delta": delta}),
	} {
		requireLiveDiffSSEUnchanged(t, transform, event)
	}
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return liveDiffPreviewAdds(preview, "first") && !preview.Complete
	})
	worker := transform.previews["close-item"]
	if worker == nil {
		t.Fatal("exec stream has no preview worker")
	}
	event := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "close-item", "call_id": "close-call", "input": fullInput})
	requireLiveDiffSSEUnchanged(t, transform, event)
	transform.Close()
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete && liveDiffPreviewAdds(preview, "CLOSE_FINAL")
	})
	if complete.Status != previewmodel.PreviewEdit {
		t.Fatalf("completion status = %q, want an edit with Complete=true", complete.Status)
	}
}
