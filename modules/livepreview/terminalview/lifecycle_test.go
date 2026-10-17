package terminalview

import (
	"slices"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestPreviewLifecycleUsesPresentationTime(t *testing.T) {
	var pane PreviewPane
	now := time.Unix(100, 0)
	first := batchTestPreview("z", "/root", "first.go")
	first.Thread, first.Turn = "main", "turn"
	second := batchTestPreview("a", "/root/child", "second.go")
	second.Thread, second.Turn = "child", "turn"
	pane.Observe(first, false, now)
	pane.Observe(second, false, now)
	pane.Tick(now.Add(RevealDelay - time.Nanosecond))
	if len(pane.Order) != 0 {
		t.Fatal("preview revealed before presentation deadline")
	}
	pane.Tick(now.Add(RevealDelay))
	if !slices.Equal(pane.Order, []string{"z", "a"}) {
		t.Fatalf("same-instant admission lost order: %v", pane.Order)
	}
	first.Complete = true
	finished := now.Add(time.Second)
	pane.Observe(first, false, finished)
	pane.Observe(first, true, finished.Add(time.Second))
	pane.Resync(previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"main": true, "child": true}}}, []string{"a"})
	pane.Tick(finished.Add(CompletionHold - time.Nanosecond))
	if pane.Views["z"] == nil {
		t.Fatal("resync retired completion before its deadline")
	}
	pane.Tick(finished.Add(CompletionHold))
	if pane.Views["z"] != nil || pane.Views["a"] == nil {
		t.Fatal("duplicate completion extended deadline or retired running sibling")
	}
	pane.Observe(first, true, finished.Add(time.Hour))
	pane.Tick(finished.Add(2 * time.Hour))
	if pane.Views["z"] != nil {
		t.Fatal("restoration reopened expired completion")
	}
	pane.EndTurn("main", "turn", finished)
	if pane.Views["a"] == nil {
		t.Fatal("Main retirement removed child")
	}
	pane.EndTurn("child", "turn", finished)
	if pane.Tick(finished.Add(time.Hour)) || len(pane.Order) != 0 {
		t.Fatal("turn retirement retained preview work")
	}
}
