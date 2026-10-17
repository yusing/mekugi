package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"

	"github.com/yusing/mekugi/modules/livepreview/projection"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotLiveDiffCompactProvisionalContext(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	before := liveDiffLinesFile("context", 40)
	after := strings.Replace(before, "context_line_20", "changed_line", 1)
	path := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"stock", "shell"} {
		t.Run(source, func(t *testing.T) {
			preview := previewmodel.Preview{ID: "edit", Workspace: workspace, Caller: "/root", Status: previewmodel.PreviewEdit, Complete: true}
			if source == "stock" {
				preview.Input = "*** Begin Patch\n*** Update File: file.txt\n@@\n-context_line_20\n+changed_line\n*** End Patch\n"
				preview = projectStockPatchPreview(t.Context(), workspace, preview)
			} else {
				files, _, recognized, err := projection.Shell(t.Context(), "cat <<'EOF' > file.txt\n"+after+"EOF\n", workspace, true)
				if err != nil || !recognized {
					t.Fatalf("shell preview: recognized=%t, err=%v", recognized, err)
				}
				preview.Files = files
			}
			if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "@@ -18,7 +18,7 @@\n") {
				t.Fatalf("provisional context is not compact: %+v", preview.Files)
			}
			var pane previewview.PreviewPane
			pane.Update(preview)
			rows, err := pane.RenderBatch(t.Context(), preview.Caller, workspace, diffrender.DarkTheme, 80, 16, 15)
			if err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/live-diff-compact-context-"+source+".txt", strings.Join(rows, "\n")+"\n")
		})
	}
}

func TestStockPatchPreviewUsesObservedSourceAndLanguageRenderer(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	before := "package sample\n\nfunc before() {}\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: sample.go\n@@\n-func before() {}\n+func after() {}\n*** End Patch\n"
	preview := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{ID: "patch", Workspace: workspace, Thread: "thread", Input: patch, Status: previewmodel.PreviewEdit, DiffText: true})
	if preview.Input != "" || len(preview.Files) != 1 || preview.Files[0].BeforePath != path || preview.Files[0].AfterPath != path {
		t.Fatalf("stock patch did not produce a file review: %+v", preview)
	}
	if diff := preview.Files[0].Diff; !strings.Contains(diff, "-func before() {}") || !strings.Contains(diff, "+func after() {}") {
		t.Fatalf("review is not the source-to-result diff: %s", diff)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != before {
		t.Fatalf("preview changed the workspace: %q, %v", got, err)
	}
	var pane previewview.PreviewPane
	pane.Update(preview)
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		lines, err := pane.RenderBatch(t.Context(), preview.Caller, workspace, theme, 100, 16, 15)
		if err != nil {
			t.Fatal(err)
		}
		frame := strings.Join(lines, "\n")
		plain := ansi.Strip(frame)
		if !strings.Contains(plain, "-func before() {}") || !strings.Contains(plain, "+func after() {}") || strings.Contains(plain, "*** Update File") {
			t.Fatalf("terminal rendered patch instructions rather than diff: %q; diff=%q", plain, preview.Files[0].Diff)
		}
		if !strings.Contains(frame, theme.Foreground(chroma.Keyword)+"func") {
			t.Fatalf("Go syntax highlighting was lost: %q", frame)
		}
	}
}

func TestStockPatchPreviewRefusesUnmatchedSource(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		Input:  "*** Begin Patch\n*** Update File: sample.go\n@@\n-not-present()\n+new()\n*** End Patch\n",
		Status: previewmodel.PreviewEdit,
	})
	if len(preview.Files) != 0 || !strings.HasPrefix(preview.Status, previewmodel.PreviewUnavailable) || preview.Input != "" {
		t.Fatalf("unmatched patch was presented as a diff: %+v", preview)
	}
}

func TestUnprojectableFinalPatchClearsPreviewWithoutClaimingFailure(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", applyPatchToolName)
	t.Cleanup(worker.Stop)
	partial := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n"
	worker.Append(partial)
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+new()")
	})
	worker.Finish(partial + "*** Update File: missing.go\n@@\n-old\n+new\n*** End Patch\n")
	clear := waitLiveDiffWorkerPreview(t, broker, sub, func(next previewmodel.Preview) bool {
		return next.ID == preview.ID && next.Complete
	})
	if clear.Status != "" || len(clear.Files) != 0 || clear.Workspace != workspace {
		t.Fatalf("unprojectable patch claimed a result: %+v", clear)
	}
	var pane previewview.PreviewPane
	pane.Update(preview)
	pane.Update(clear)
	if len(pane.Views) != 0 {
		t.Fatal("unprojectable patch left a stale card")
	}
}

func TestPendingPatchBetweenEditsKeepsLastProjectedDiff(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for name, content := range map[string]string{"first.go": "old()\n", "second.go": "before()\n"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	first := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: "sequence", Workspace: workspace, Thread: "thread", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+new()\n*** End Patch\n",
	})
	broker.Update(first, false)
	testReview(broker).TakePreviews(sub)
	pending := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: first.ID, Workspace: workspace, Thread: "thread", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: second.go\n",
	})
	if pending.Input != "\n" || len(pending.Files) != 0 {
		t.Fatalf("partial patch did not produce the pending marker: %+v", pending)
	}
	broker.Update(pending, false)
	batch := testReview(broker).TakePreviews(sub)
	if len(batch) != 1 || batch[0].Preview == nil {
		t.Fatalf("pending edit lost preview event: %+v", batch)
	}
	between := *batch[0].Preview
	if between.Input != "" || len(between.Files) != 1 || !strings.Contains(between.Files[0].Diff, "+new()") {
		t.Fatalf("pending edit blanked the previous diff: %+v", between)
	}
	var pane previewview.PreviewPane
	pane.Update(first)
	pane.Update(between)
	lines, err := pane.RenderBatch(t.Context(), first.Caller, workspace, diffrender.DarkTheme, 100, 16, 15)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+new()") {
		t.Fatalf("stream turned blank between edits: %q, %v", lines, err)
	}
	second := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: first.ID, Workspace: workspace, Thread: "thread", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: second.go\n@@\n-before()\n+after()\n*** End Patch\n",
	})
	broker.Update(second, false)
	batch = testReview(broker).TakePreviews(sub)
	if len(batch) != 1 || batch[0].Preview == nil || len(batch[0].Preview.Files) != 1 ||
		!strings.Contains(batch[0].Preview.Files[0].Diff, "+after()") || strings.Contains(batch[0].Preview.Files[0].Diff, "+new()") {
		t.Fatalf("next edit did not replace the retained diff: %+v", batch)
	}
}

func TestNextCallPendingPatchKeepsCompletedDiffUntilItHasContent(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "first.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: "first", Workspace: workspace, Thread: "thread", Caller: "/root", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+new()\n*** End Patch\n",
	})
	first.Complete = true
	var pane previewview.PreviewPane
	pane.Update(first)
	pending := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: "next", Workspace: workspace, Thread: "thread", Caller: "/root", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n",
	})
	for _, input := range []string{
		"*** Begin Patch\n*** Add File: next.go\n",
		"*** Begin Patch\n*** Add File: next.go\n+",
	} {
		previewContext := t.Context()
		previewPatch := previewProjector(previewContext, applyPatchToolName)
		adding, _ := previewPatch(input, workspace, false)
		adding.ID, adding.Workspace, adding.Caller = pending.ID, workspace, "/root"
		pane.Update(adding)
		if len(pane.Order) != 1 || pane.Order[0] != first.ID {
			t.Fatalf("add header replaced completed edit: %v", pane.Order)
		}
	}
	pane.Update(pending)
	if len(pane.Order) != 1 || pane.Order[0] != first.ID {
		t.Fatalf("empty next call replaced completed edit: %v", pane.Order)
	}
	lines, err := pane.RenderBatch(t.Context(), first.Caller, workspace, diffrender.DarkTheme, 100, 16, 15)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+new()") {
		t.Fatalf("next call blanked previous diff: %q, %v", lines, err)
	}
	next := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: pending.ID, Workspace: workspace, Thread: "thread", Caller: "/root", Status: previewmodel.PreviewEdit,
		Input: "*** Begin Patch\n*** Update File: first.go\n@@\n-old()\n+later()\n*** End Patch\n",
	})
	pane.Update(next)
	if len(pane.Order) != 2 || pane.Order[1] != next.ID || len(pane.Views[next.ID].Current.Files) != 1 {
		t.Fatalf("next projected edit lost the settling call: %v", pane.Order)
	}
	lines, err = pane.RenderBatch(t.Context(), next.Caller, workspace, diffrender.DarkTheme, 100, 16, 15)
	if err != nil || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "+later()") {
		t.Fatalf("next projected edit not displayed: %q, %v", lines, err)
	}
}

func TestStockPatchStreamingPartialLinesStayProjectable(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previewContext := t.Context()
	previewPatch := previewProjector(previewContext, applyPatchToolName)
	for _, fragment := range []string{
		"*** Begin Patch",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new",
		"*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Pat",
	} {
		preview, ok := previewPatch(fragment, workspace, false)
		if !ok || strings.HasPrefix(preview.Status, previewmodel.PreviewUnavailable) {
			t.Fatalf("partial patch became unavailable: %+v, recognized=%t", preview, ok)
		}
	}
	complete := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n"
	preview, ok := previewPatch(complete, workspace, true)
	if !ok || len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+new()") {
		t.Fatalf("final patch was not projected: %+v, recognized=%t", preview, ok)
	}
}

func TestStockPatchStreamingFramesEndAtTheirTip(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("a()\nb()\nc()\nd()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previewContext := t.Context()
	previewPatch := previewProjector(previewContext, applyPatchToolName)
	// Source past the streamed tip is not context yet: the next line may
	// remove it, and trailing context would renumber under each added row.
	preview, ok := previewPatch("*** Begin Patch\n*** Update File: sample.go\n@@\n a()\n+new()\n", workspace, false)
	if !ok || len(preview.Files) != 1 {
		t.Fatalf("partial patch was not projected: %+v", preview)
	}
	if diff := preview.Files[0].Diff; !strings.Contains(diff, "+new()") || strings.Contains(diff, "b()") {
		t.Fatalf("partial frame showed source past its tip: %q", diff)
	}
	preview, ok = previewPatch("*** Begin Patch\n*** Update File: sample.go\n@@\n a()\n+new()\n-b()\n*** End Patch\n", workspace, true)
	if diff := preview.Files[0].Diff; !ok || !strings.Contains(diff, "-b()") || !strings.Contains(diff, " c()") {
		t.Fatalf("complete patch lost its trailing context: %q", diff)
	}
}

func TestStockPatchPreviewKeepsSourceFromBeforeTheCall(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previewContext := projection.WithSources(t.Context(), false)
	previewPatch := previewProjector(previewContext, applyPatchToolName)
	if _, ok := previewPatch("*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n", workspace, false); !ok {
		t.Fatal("partial patch was not projected")
	}
	// The host applies the call while its final frame is still pacing.
	if err := os.WriteFile(path, []byte("new()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, ok := previewPatch("*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n", workspace, true)
	if !ok || len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "-old()") {
		t.Fatalf("final projection re-read the applied file: %+v", preview)
	}
}

func TestCodeModePatchFinalPreviewUsesPreExecutionSource(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t)
	broker := newLivePreview(t.Context())
	proxy.review = testReview(broker)
	attachTestReplayStore(t, proxy)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	path := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(path, []byte("old()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()

	proxy.review.Preview().Enable()
	patch := "*** Begin Patch\n*** Update File: sample.go\n@@\n-old()\n+new()\n*** End Patch\n"
	script := "const patch = " + string(responses.MustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));"
	for _, event := range []map[string]any{
		{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
			"type": "custom_tool_call", "id": "code-item", "call_id": "code-call", "name": "exec",
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
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "-old()") ||
		!strings.Contains(preview.Files[0].Diff, "+new()") || strings.HasPrefix(preview.Status, previewmodel.PreviewUnavailable) {
		t.Fatalf("final preview did not use pre-execution source: %+v", preview)
	}
}

func TestStockPatchPreviewBlankContextAndInsertion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ before, patch, removed, added string }{
		{"a()\n\na()\n", "@@\n\n-a()\n+b()", "-a()", "+b()"},
		{"first\n\n", "@@\n+added", "", "+added"},
	} {
		workspace := t.TempDir()
		path := filepath.Join(workspace, "file.txt")
		if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
			t.Fatal(err)
		}
		preview := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
			Input:  "*** Begin Patch\n*** Update File: file.txt\n" + tc.patch + "\n*** End Patch\n",
			Status: previewmodel.PreviewEdit,
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
	t.Parallel()
	workspace := t.TempDir()
	preview := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		Input:  "*** Begin Patch\r\n*** Add File: crlf.go\r\n+package crlf\r\n*** End Patch\r\n",
		Status: previewmodel.PreviewEdit,
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+package crlf") {
		t.Fatalf("CRLF stock patch lost its diff projection: %+v", preview)
	}
}

func TestStockPatchFinalPreviewSurvivesTransportCancellation(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.go"), []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	ctx, cancel := context.WithCancel(t.Context())
	worker := startLiveDiffPreview(ctx, broker, workspace, "thread", applyPatchToolName)
	t.Cleanup(worker.Stop)
	worker.Finish("*** Begin Patch\n*** Update File: file.go\n@@\n-package old\n+package new\n*** End Patch\n")
	cancel()
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Complete && len(preview.Files) == 1
	})
	if !strings.Contains(preview.Files[0].Diff, "+package new") || preview.Status != previewmodel.PreviewEdit {
		t.Fatalf("final stock preview lost after cancellation: %+v", preview)
	}
}

func waitLiveDiffWorkerPreview(t *testing.T, broker *previewmodel.Session, sub *sessionreview.Subscription, match func(previewmodel.Preview) bool) previewmodel.Preview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-sub.Ready():
			for _, event := range testReview(broker).TakePreviews(sub) {
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
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.Stop)

	patch := "*** Begin Patch\n*** Add File: result.txt\n+content\n*** End Patch\n"
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	source := "const patch = " + string(encoded) + "; text(await tools.apply_patch(patch));"
	marker := strings.Index(source, "*** Begin Patch")
	worker.Append(source[:marker+len("*** Begin Patch")])
	select {
	case <-sub.Ready():
		for _, event := range testReview(broker).TakePreviews(sub) {
			if event.Preview != nil && strings.Contains(event.Preview.Input, "*** Begin Patch") && !event.Preview.DiffText {
				t.Fatalf("partial exec patch leaked as script: %+v", event.Preview)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("partial exec patch did not update its live preview")
	}

	worker.Append(source[marker+len("*** Begin Patch"):])
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+content")
	})
	if preview.Input != "" || preview.Files[0].BeforePath != "" || strings.Contains(preview.Files[0].Diff, "const patch") {
		t.Fatalf("exec patch preview leaked script encoding: %+v", preview)
	}
}

func TestLiveDiffCodeModeEscapedPatchMarkerDoesNotLeakScript(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.Stop)

	source := `const emoji = "\u{1F600}"; const label = "don\'t\/stop"; const patch = "\x2a** Begin Patch\n*** Add File: result.txt\n+content\n*** End Patch\n"; text(await tools.apply_patch(patch));`
	marker := strings.Index(source, `\x2a`)
	closingQuote := marker + strings.Index(source[marker:], `";`)
	worker.Append(source[:closingQuote])
	// Paced frames reveal the JavaScript before the patch marker, as token
	// streaming would. Once the marker is revealed, the script stays hidden.
	revealed := false
	for !revealed {
		select {
		case <-sub.Ready():
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview == nil {
					continue
				}
				if strings.Contains(event.Preview.Input, "const") {
					t.Fatalf("escaped exec patch leaked as script: %+v", event.Preview)
				}
				revealed = revealed || event.Preview.Workspace == "" || len(event.Preview.Files) != 0
			}
		case <-time.After(5 * time.Second):
			t.Fatal("escaped exec patch did not update its live preview")
		}
	}
	worker.Append(source[closingQuote:])
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+content")
	})
	if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "+content") {
		t.Fatalf("escaped exec patch was not decoded as a patch preview: %+v", preview)
	}
}

func newLiveDiffWorkerTest(t *testing.T, workspace string) (*previewmodel.Session, *sessionreview.Subscription, *previewbinding.Stream) {
	t.Helper()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.Stop)
	return broker, sub, worker
}
