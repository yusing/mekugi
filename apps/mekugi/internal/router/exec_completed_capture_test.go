package router

import (
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestExecCompletedReceiptKeepsPythonValidation(t *testing.T) {
	writer, check := "cat > source.go", "python3 -c 'assert True'"
	command := writer + "; " + check
	entry := activityPaneEntry{Kind: "tool", Text: toolActivityShell(command), native: &liveActivityNativeItem{
		thread: "main", item: "cmd", command: command,
		segments:     []commandSegment{{source: writer, text: execSegmentText(writer)}, {source: check, text: execSegmentText(check)}},
		capturedEdit: &presentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `source.go` +1 -1 · cat"},
	}}
	blocks := commandSegmentBlocks(commandPresentation(entry))
	if !slices.ContainsFunc(blocks, func(block activityui.Block) bool {
		return block.Code == check || strings.Contains(block.Label, "assert True")
	}) {
		t.Fatalf("receipt removed the Python validation: %+v", blocks)
	}
}

func TestNativeCompletedReceiptWithdrawal(t *testing.T) {
	u, _ := newTrackedAppServerUI(t)
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "edit", Type: "commandExecution", Command: "python3 edit.py; read -r answer", Status: "inProgress"}})
	frame := previewmodel.Preview{ID: "observed:cell", Workspace: u.session.cwd, Thread: "main", Turn: "turn", Complete: true, HostItems: []string{"edit"}, EditReceipt: "Edit `a.go` +1 -1 · python3", Footer: "observed edit"}
	u.shell.preview(frame, false)
	awaitMain(t, u, "Edited")
	frame.EditReceipt = ""
	u.shell.preview(frame, false)
	for _, view := range []*activityAdapter{u.view, u.agents} {
		for _, entry := range view.Entries {
			if entry.Source.native != nil && entry.Source.native.capturedEdit != nil {
				t.Fatal("UI retained a withdrawn receipt")
			}
		}
	}
}
