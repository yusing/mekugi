package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestStockPatchPreviewUsesObservedSourceAndLanguageRenderer(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	before := "package sample\n\nfunc before() {}\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: sample.go\n@@\n-func before() {}\n+func after() {}\n*** End Patch\n"
	preview := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{ID: "patch", Workspace: workspace, Thread: "thread", Input: patch, Status: liveDiffPreviewEdit, DiffText: true})
	if preview.Input != "" || len(preview.Files) != 1 || preview.Files[0].BeforePath != path || preview.Files[0].AfterPath != path {
		t.Fatalf("stock patch did not produce a file review: %+v", preview)
	}
	if diff := preview.Files[0].Diff; !strings.Contains(diff, "-func before() {}") || !strings.Contains(diff, "+func after() {}") {
		t.Fatalf("review is not the source-to-result diff: %s", diff)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != before {
		t.Fatalf("preview changed the workspace: %q, %v", got, err)
	}
	var pane liveDiffPreviewPane
	pane.update(preview)
	for _, theme := range []liveDiffTheme{livediff.DarkTheme, livediff.LightTheme} {
		lines, err := pane.render(t.Context(), workspace, theme, 100, 12)
		if err != nil {
			t.Fatal(err)
		}
		frame := strings.Join(lines, "\n")
		plain := ansi.Strip(frame)
		if !strings.Contains(plain, "-func before() {}") || !strings.Contains(plain, "+func after() {}") || strings.Contains(plain, "*** Update File") {
			t.Fatalf("terminal rendered patch instructions rather than diff: %q; rows=%+v; diff=%q", plain, pane.views["patch"].source, preview.Files[0].Diff)
		}
		if !strings.Contains(frame, theme.Foreground(chroma.Keyword)+"func") {
			t.Fatalf("Go syntax highlighting was lost: %q", frame)
		}
	}
}

func TestStockPatchPreviewRefusesUnmatchedSource(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		Input:  "*** Begin Patch\n*** Update File: sample.go\n@@\n-not-present()\n+new()\n*** End Patch\n",
		Status: liveDiffPreviewEdit,
	})
	if len(preview.Files) != 0 || !strings.HasPrefix(preview.Status, liveDiffPreviewUnavailable) || preview.Input != "" {
		t.Fatalf("unmatched patch was presented as a diff: %+v", preview)
	}
}

func TestUnprojectableFinalPatchClearsPreviewWithoutClaimingFailure(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", applyPatchToolName)
	t.Cleanup(worker.stop)
	partial := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n"
	worker.appendDelta(partial)
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+new()")
	})
	worker.finish(partial + "*** Update File: missing.go\n@@\n-old\n+new\n*** End Patch\n")
	clear := waitLiveDiffWorkerPreview(t, broker, sub, func(next liveDiffPreview) bool {
		return next.ID == preview.ID && next.Complete
	})
	if clear.Status != "" || len(clear.Files) != 0 || clear.Workspace != workspace {
		t.Fatalf("unprojectable patch claimed a result: %+v", clear)
	}
	var pane liveDiffPreviewPane
	pane.update(preview)
	pane.update(clear)
	if len(pane.views) != 0 {
		t.Fatal("unprojectable patch left a stale card")
	}
}

func TestPendingPatchBetweenEditsKeepsLastProjectedDiff(t *testing.T) {
	workspace := t.TempDir()
	for name, content := range map[string]string{"first.go": "old()\n", "second.go": "before()\n"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	first := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: "sequence", Workspace: workspace, Thread: "thread", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+new()\n*** End Patch\n",
	})
	broker.publishPreview(first, false)
	broker.takePreviews(sub)
	pending := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: first.ID, Workspace: workspace, Thread: "thread", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: second.go\n",
	})
	if pending.Input != "\n" || len(pending.Files) != 0 {
		t.Fatalf("partial patch did not produce the pending marker: %+v", pending)
	}
	broker.publishPreview(pending, false)
	batch := broker.takePreviews(sub)
	if len(batch) != 1 || batch[0].Preview == nil {
		t.Fatalf("pending edit lost preview event: %+v", batch)
	}
	between := *batch[0].Preview
	if between.Input != "" || len(between.Files) != 1 || !strings.Contains(between.Files[0].Diff, "+new()") {
		t.Fatalf("pending edit blanked the previous diff: %+v", between)
	}
	var pane liveDiffPreviewPane
	pane.update(first)
	pane.update(between)
	lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+new()") {
		t.Fatalf("stream turned blank between edits: %q, %v", lines, err)
	}
	second := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: first.ID, Workspace: workspace, Thread: "thread", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: second.go\n@@\n-before()\n+after()\n*** End Patch\n",
	})
	broker.publishPreview(second, false)
	batch = broker.takePreviews(sub)
	if len(batch) != 1 || batch[0].Preview == nil || len(batch[0].Preview.Files) != 1 ||
		!strings.Contains(batch[0].Preview.Files[0].Diff, "+after()") || strings.Contains(batch[0].Preview.Files[0].Diff, "+new()") {
		t.Fatalf("next edit did not replace the retained diff: %+v", batch)
	}
}

func TestNextCallPendingPatchKeepsCompletedDiffUntilItHasContent(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "first.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: "first", Workspace: workspace, Thread: "thread", Caller: "/root", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+new()\n*** End Patch\n",
	})
	first.Complete = true
	var pane liveDiffPreviewPane
	pane.update(first)
	pending := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: "next", Workspace: workspace, Thread: "thread", Caller: "/root", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n",
	})
	for _, input := range []string{
		"*** Begin Patch\n*** Add File: next.go\n",
		"*** Begin Patch\n*** Add File: next.go\n+",
	} {
		worker := liveDiffPreviewWorker{ctx: t.Context()}
		adding, _ := worker.projectStockPreview(input, workspace, false)
		adding.ID, adding.Workspace, adding.Caller = pending.ID, workspace, "/root"
		pane.update(adding)
		if len(pane.order) != 1 || pane.order[0] != first.ID {
			t.Fatalf("add header replaced completed edit: %v", pane.order)
		}
	}
	pane.update(pending)
	if len(pane.order) != 1 || pane.order[0] != first.ID {
		t.Fatalf("empty next call replaced completed edit: %v", pane.order)
	}
	lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+new()") {
		t.Fatalf("next call blanked previous diff: %q, %v", lines, err)
	}
	next := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		ID: pending.ID, Workspace: workspace, Thread: "thread", Caller: "/root", Status: liveDiffPreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+later()\n*** End Patch\n",
	})
	pane.update(next)
	if len(pane.order) != 1 || pane.order[0] != next.ID || len(pane.views[next.ID].current.Files) != 1 {
		t.Fatalf("next projected edit did not take over: %v", pane.order)
	}
	lines, err = pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 10)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+later()") {
		t.Fatalf("next projected edit not displayed: %q, %v", lines, err)
	}
}

func TestStockPatchStreamingPartialLinesStayProjectable(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &liveDiffPreviewWorker{ctx: t.Context()}
	for _, fragment := range []string{
		"*** Begin Patch",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Pat",
	} {
		preview, ok := worker.projectStockPreview(fragment, workspace, false)
		if !ok || strings.HasPrefix(preview.Status, liveDiffPreviewUnavailable) {
			t.Fatalf("partial patch became unavailable: %+v, recognized=%t", preview, ok)
		}
	}
	complete := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n"
	preview, ok := worker.projectStockPreview(complete, workspace, true)
	if !ok || len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+new()") {
		t.Fatalf("final patch was not projected: %+v, recognized=%t", preview, ok)
	}
}

func TestStockPatchStreamingFramesEndAtTheirTip(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("a()\nb()\nc()\nd()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &liveDiffPreviewWorker{ctx: t.Context()}
	// Source past the streamed tip is not context yet: the next line may
	// remove it, and trailing context would renumber under each added row.
	preview, ok := worker.projectStockPreview("*** Begin Patch\n*** Update File: sample.go\n@@\n a()\n+new()\n", workspace, false)
	if !ok || len(preview.Files) != 1 {
		t.Fatalf("partial patch was not projected: %+v", preview)
	}
	if diff := preview.Files[0].Diff; !strings.Contains(diff, "+new()") || strings.Contains(diff, "b()") {
		t.Fatalf("partial frame showed source past its tip: %q", diff)
	}
	preview, ok = worker.projectStockPreview("*** Begin Patch\n*** Update File: sample.go\n@@\n a()\n+new()\n-b()\n*** End Patch\n", workspace, true)
	if diff := preview.Files[0].Diff; !ok || !strings.Contains(diff, "-b()") || !strings.Contains(diff, " c()") {
		t.Fatalf("complete patch lost its trailing context: %q", diff)
	}
}

func TestStockPatchPreviewKeepsSourceFromBeforeTheCall(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &liveDiffPreviewWorker{ctx: withLiveDiffSources(t.Context())}
	if _, ok := worker.projectStockPreview("*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n", workspace, false); !ok {
		t.Fatal("partial patch was not projected")
	}
	// The host applies the call while its final frame is still pacing.
	if err := os.WriteFile(path, []byte("new()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, ok := worker.projectStockPreview("*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n", workspace, true)
	if !ok || len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "-old()") {
		t.Fatalf("final projection re-read the applied file: %+v", preview)
	}
}

func TestCodeModePatchFinalPreviewUsesPreExecutionSource(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {transform.threadID: true}}})
	sub := broker.subscribe()
	<-sub.events
	proxy.autoLiveDiff = &autoLiveDiff{events: broker, requested: true,
		scope: liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {transform.threadID: true}}}}
	proxy.autoLiveDiff.enabled.Store(true)
	patch := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n"
	script := "const patch = " + string(mustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));"
	for _, event := range []map[string]any{
		{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
			"type": "custom_tool_call", "id": "code-item", "call_id": "code-call", "name": transform.codeModeToolName,
			"input": "", "status": "in_progress"}},
		{"type": "response.custom_tool_call_input.delta", "item_id": "code-item", "delta": script},
		{"type": "response.custom_tool_call_input.done", "item_id": "code-item", "call_id": "code-call", "input": script},
	} {
		if _, err := transform.TransformSSE(mustTestJSON(t, event)); err != nil {
			t.Fatal(err)
		}
	}
	// The host runs after input.done, while the preview worker can still be
	// revealing the call. Its final frame must use the captured old source.
	if err := os.WriteFile(path, []byte("new()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "-old()") ||
		!strings.Contains(preview.Files[0].Diff, "+new()") || strings.HasPrefix(preview.Status, liveDiffPreviewUnavailable) {
		t.Fatalf("final preview did not use pre-execution source: %+v", preview)
	}
}

func TestStockPatchPreviewBlankContextAndInsertion(t *testing.T) {
	for _, tc := range []struct{ before, patch, removed, added string }{
		{"a()\n\na()\n", "@@\n\n-a()\n+b()", "-a()", "+b()"},
		{"first\n\n", "@@\n+added", "", "+added"},
	} {
		workspace := t.TempDir()
		path := filepath.Join(workspace, "file.txt")
		if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
			t.Fatal(err)
		}
		preview := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
			Input:  "*** Begin Patch\n*** Update File: file.txt\n" + tc.patch + "\n*** End Patch\n",
			Status: liveDiffPreviewEdit,
		})
		if len(preview.Files) != 1 {
			t.Fatalf("stock patch context failed projection: %+v", preview)
		}
		diff := preview.Files[0].Diff
		if tc.removed != "" && !strings.Contains(diff, tc.removed) || !strings.Contains(diff, tc.added) {
			t.Fatalf("incorrect source projection: %q", diff)
		}
		if tc.removed == "" && !strings.Contains(diff, " first\n-\n+added\n") {
			t.Fatalf("insertion moved past final blank line: %q", diff)
		}
		if tc.removed != "" && !strings.Contains(diff, " a()\n \n-a()\n+b()\n") {
			t.Fatalf("unprefixed blank context changed the wrong duplicate: %q", diff)
		}
	}
}

func TestStockPatchPreviewAcceptsCRLFEnvelope(t *testing.T) {
	workspace := t.TempDir()
	preview := projectStockPatchPreview(t.Context(), workspace, liveDiffPreview{
		Input:  "*** Begin Patch\r\n*** Add File: crlf.go\r\n+package crlf\r\n*** End Patch\r\n",
		Status: liveDiffPreviewEdit,
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+package crlf") {
		t.Fatalf("CRLF stock patch lost its diff projection: %+v", preview)
	}
}

func TestStockPatchFinalPreviewSurvivesTransportCancellation(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.go"), []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	ctx, cancel := context.WithCancel(t.Context())
	worker := startLiveDiffPreview(ctx, broker, workspace, "thread", applyPatchToolName)
	t.Cleanup(worker.stop)
	worker.finish("*** Begin Patch\n*** Update File: file.go\n@@\n-package old\n+package new\n*** End Patch\n")
	cancel()
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Complete && len(preview.Files) == 1
	})
	if !strings.Contains(preview.Files[0].Diff, "+package new") || preview.Status != liveDiffPreviewEdit {
		t.Fatalf("final stock preview lost after cancellation: %+v", preview)
	}
}

func waitLiveDiffWorkerPreview(t *testing.T, broker *liveDiffBroker, sub *liveDiffSubscriber, match func(liveDiffPreview) bool) liveDiffPreview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-sub.previewReady:
			for _, event := range broker.takePreviews(sub) {
				if event.Preview != nil && match(*event.Preview) {
					return *event.Preview
				}
			}
		case <-timer.C:
			t.Fatal("missing live diff worker preview")
		}
	}
}

func TestLiveDiffCodeModeConstPatchDoesNotLeakScript(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)

	patch := "*** Begin Patch\n*** Add File: result.txt\n+content\n*** End Patch\n"
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	source := "const patch = " + string(encoded) + "; text(await tools.apply_patch(patch));"
	marker := strings.Index(source, "*** Begin Patch")
	worker.appendDelta(source[:marker+len("*** Begin Patch")])
	select {
	case <-sub.previewReady:
		for _, event := range broker.takePreviews(sub) {
			if event.Preview != nil && strings.Contains(event.Preview.Input, "*** Begin Patch") && !event.Preview.DiffText {
				t.Fatalf("partial Code Mode patch leaked as script: %+v", event.Preview)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("partial Code Mode patch did not update its live preview")
	}

	worker.appendDelta(source[marker+len("*** Begin Patch"):])
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+content")
	})
	if preview.Input != "" || preview.Files[0].BeforePath != "" || strings.Contains(preview.Files[0].Diff, "const patch") {
		t.Fatalf("Code Mode patch preview leaked script encoding: %+v", preview)
	}
}

func TestLiveDiffCodeModeEscapedPatchMarkerDoesNotLeakScript(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)

	source := `const emoji = "\u{1F600}"; const label = "don\'t\/stop"; const patch = "\x2a** Begin Patch\n*** Add File: result.txt\n+content\n*** End Patch\n"; text(await tools.apply_patch(patch));`
	marker := strings.Index(source, `\x2a`)
	closingQuote := marker + strings.Index(source[marker:], `";`)
	worker.appendDelta(source[:closingQuote])
	// Paced frames reveal the JavaScript before the patch marker, as token
	// streaming would. Once the marker is revealed, the script stays hidden.
	revealed := false
	for !revealed {
		select {
		case <-sub.previewReady:
			for _, event := range broker.takePreviews(sub) {
				if event.Preview == nil {
					continue
				}
				if strings.Contains(event.Preview.Input, "const") {
					t.Fatalf("escaped Code Mode patch leaked as script: %+v", event.Preview)
				}
				revealed = revealed || event.Preview.Workspace == "" || len(event.Preview.Files) != 0
			}
		case <-time.After(5 * time.Second):
			t.Fatal("escaped Code Mode patch did not update its live preview")
		}
	}
	worker.appendDelta(source[closingQuote:])
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+content")
	})
	if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "+content") {
		t.Fatalf("escaped Code Mode patch was not decoded as a patch preview: %+v", preview)
	}
}

func newLiveDiffWorkerTest(t *testing.T, workspace string) (*liveDiffBroker, *liveDiffSubscriber, *liveDiffPreviewWorker) {
	t.Helper()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)
	return broker, sub, worker
}
