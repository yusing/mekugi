package router

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
		return preview.Status == "STREAMING PREVIEW" && preview.DiffText && preview.Input == patch
	})
	if preview.Input != patch || strings.Contains(preview.Input, "const patch") || strings.Contains(preview.Input, `\\n`) {
		t.Fatalf("Code Mode patch preview leaked script encoding: %+v", preview)
	}
}

func TestLiveDiffCodeModeEscapedPatchMarkerDoesNotLeakScript(t *testing.T) {
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
	select {
	case <-sub.previewReady:
		for _, event := range broker.takePreviews(sub) {
			if event.Preview != nil && event.Preview.Status == "STREAMING SCRIPT" {
				t.Fatalf("escaped Code Mode patch leaked as script: %+v", event.Preview)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("escaped Code Mode patch did not update its live preview")
	}
	worker.appendDelta(source[closingQuote:])
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == "STREAMING PREVIEW" && preview.DiffText
	})
	if strings.Contains(preview.Input, `\x2a`) || !strings.HasPrefix(preview.Input, "*** Begin Patch\n") {
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
