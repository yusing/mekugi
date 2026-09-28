package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveActivityAttachmentOutcomes(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "attached.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("private file contents\n", 1500)), 0600); err != nil {
		t.Fatal(err)
	}
	draft := composerDraft{text: "Review files", files: []composerFile{{path: path}, {path: filepath.Join(cwd, "missing.txt")}}}
	draft.snapshotFileAttachments(cwd)
	content, err := json.Marshal(draft.input())
	if err != nil {
		t.Fatal(err)
	}
	item := appServerItem{Type: "userMessage", Content: content, ID: "input"}
	u, _ := newAppServerTestUI()
	for _, method := range []string{"item/started", "item/completed", "item/completed"} {
		u.view.applyAppServerItem(u.thread, u.thread, "turn", item.ID, method, "", item)
	}
	u.view.applyAppServerItem(u.thread, u.thread, "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Agent response"})
	if len(u.view.entries) != 3 || u.view.entries[1].Kind != "attachments" {
		t.Fatalf("missing, duplicated, or reordered receipts: %+v", u.view.entries)
	}
	blocks := u.view.blocks[1]
	if blocks[0].Path != path {
		t.Fatalf("attachment path is not literal: %+v", blocks[0])
	}
	if len(blocks) != 2 || blocks[0].Verb != "Attached" || blocks[1].Verb != "Attach failed" || !strings.Contains(blocks[1].Label, "no such file") {
		t.Fatalf("incorrect outcomes: %+v", blocks)
	}
	var screen bytes.Buffer
	if err := u.paint(&screen, 300, 35); err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(screen.String())
	if !strings.Contains(plain, path) || strings.Contains(plain, `"`+path+`"`) {
		t.Fatalf("attachment path not displayed unquoted: %s", plain)
	}
	attached, failed, response := strings.Index(plain, "Attached"), strings.Index(plain, "Attach failed"), strings.Index(plain, "Agent response")
	if attached < 0 || failed < attached || response < failed || strings.Contains(plain, "private file contents") || strings.Contains(plain, fileAttachmentPrefix) {
		t.Fatalf("incorrect receipt rendering/order: %s", plain)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	resumed, _ := newAppServerTestUI()
	resumed.agents = newLiveActivityView()
	resumed.session.start(resumed.thread, cwd)
	resumed.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
	if len(resumed.view.entries) != 2 || resumed.view.blocks[1][0].Verb != "Attached" || resumed.view.blocks[1][1].Verb != "Attach failed" {
		t.Fatal("resume did not preserve submitted outcomes")
	}
}

func TestLiveActivityAttachmentLookalikes(t *testing.T) {
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": "Attached file \"fake\" (UTF-8 bytes 0:1 of 1):\nx"}})
	if blocks := appServerAttachmentBlocks(content); len(blocks) != 0 {
		t.Fatalf("ordinary text became attachment receipts: %+v", blocks)
	}
}
