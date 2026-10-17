package router

import (
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestExecCompletedReceiptKeepsPythonValidation(t *testing.T) {
	writer, check := "cat > source.go", "python3 -c 'assert True'"
	command := writer + "; " + check
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Text: toolActivityShell(command), Native: &sessionpresentation.NativeItem{
		Thread: "main", Item: "cmd", Command: command,
		Segments:     []sessionpresentation.ActivitySegment{{Source: writer, Text: execSegmentText(writer)}, {Source: check, Text: execSegmentText(check)}},
		CapturedEdit: &presentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `source.go` +1 -1 · cat"},
	}}
	blocks := commandSegmentBlocks(sessionpresentation.CommandPresentation(entry))
	if !slices.ContainsFunc(blocks, func(block activityui.Block) bool {
		return block.Code == check || strings.Contains(block.Label, "assert True")
	}) {
		t.Fatalf("receipt removed the Python validation: %+v", blocks)
	}
}

func TestNativeCompletedReceiptWithdrawal(t *testing.T) {
	u, _ := newTrackedAppServerUI(t)
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "edit", Type: "commandExecution", Command: "python3 edit.py; read -r answer", Status: "inProgress"}})
	frame := previewmodel.Preview{ID: "observed:cell", Workspace: u.cwd, Thread: "main", Turn: "turn", Complete: true, HostItems: []string{"edit"}, EditReceipt: "Edit `a.go` +1 -1 · python3", Footer: "observed edit"}
	u.preview(frame, false)
	awaitMain(t, u, "Edited")
	frame.EditReceipt = ""
	u.preview(frame, false)
	for _, view := range []*activityAdapter{u.testActivity(u.view), u.testActivity(u.agents)} {
		for _, entry := range view.Entries {
			if entry.Source.Native != nil && entry.Source.Native.CapturedEdit != nil {
				t.Fatal("UI retained a withdrawn receipt")
			}
		}
	}
}
