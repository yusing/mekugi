package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffPreWriteLateSubscribeAndRetentionBounds(t *testing.T) {
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{"workspace": {"thread": true}}})
	for i := range 20 {
		preview := liveDiffPreview{ID: fmt.Sprint(i), Workspace: "workspace", Thread: "thread",
			Evaluated: true, Status: "PRE-WRITE DIFF · no changes"}
		broker.publishPreview(preview, false)
		preview.Complete = true
		broker.publishPreview(preview, false)
	}
	sub := broker.subscribe()
	var completed []liveDiffPreview
	for _, event := range broker.takePreviews(sub) {
		if event.Preview != nil {
			completed = append(completed, *event.Preview)
		}
	}
	if len(completed) != 16 || completed[0].ID != "4" || completed[15].ID != "19" {
		t.Fatalf("late subscriber completed snapshots: %+v", completed)
	}
	for i := range 16 {
		broker.publishPreview(liveDiffPreview{ID: fmt.Sprint("active", i), Workspace: "workspace",
			Thread: "thread", Evaluated: true, Status: "PRE-WRITE DIFF · no changes"}, false)
	}
	broker.mu.Lock()
	activeCount := len(broker.previews)
	broker.mu.Unlock()
	if activeCount != 16 {
		t.Fatalf("completed retention consumed active capacity: %d", activeCount)
	}
	broker.publishTurn(true)
	broker.mu.Lock()
	retained := len(broker.completedPreviews)
	broker.mu.Unlock()
	if retained != 0 {
		t.Fatalf("next turn retained %d previous-turn completions", retained)
	}
}

func TestLiveDiffPreWriteDistinctCompletionsRenderBeforeEviction(t *testing.T) {
	var pane liveDiffPreviewPane
	for _, id := range []string{"first", "second"} {
		pane.update(liveDiffPreview{ID: id, Workspace: "workspace", Thread: "thread",
			Evaluated: true, Complete: true, DiffText: true, Input: "+" + id,
			Status: "PRE-WRITE DIFF"})
	}
	lines, err := pane.render(t.Context(), "workspace", livediff.DarkTheme, 100, 20)
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "+first") ||
		!strings.Contains(strings.Join(lines, "\n"), "+second") {
		t.Fatalf("distinct completions lost before frame: %v %q", err, lines)
	}
	pane.update(liveDiffPreview{ID: "next", Workspace: "workspace", Thread: "thread",
		Evaluated: true, Status: "PRE-WRITE DIFF · no changes"})
	if len(pane.order) != 1 || pane.order[0] != "next" {
		t.Fatalf("displayed completions not evicted: %v", pane.order)
	}
}
