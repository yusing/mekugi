package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
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
		u.view.applyAppServerItem(cwd, u.thread, u.thread, "turn", item.ID, method, "", item)
	}
	u.view.applyAppServerItem(cwd, u.thread, u.thread, "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Agent response"})
	if len(u.view.entries) != 3 || u.view.entries[1].Kind != "attachments" {
		t.Fatalf("missing, duplicated, or reordered receipts: %+v", u.view.entries)
	}
	blocks := u.view.entries[1].blocks
	if blocks[0].Path != "attached.txt" {
		t.Fatalf("attachment path is not workspace-relative: %+v", blocks[0])
	}
	if len(blocks) != 2 || blocks[0].Verb != "Attached" || blocks[1].Verb != "Attach failed" || !strings.Contains(blocks[1].Label, "no such file") {
		t.Fatalf("incorrect outcomes: %+v", blocks)
	}
	var screen bytes.Buffer
	if err := u.paint(&screen, 300, 35); err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(screen.String())
	if !strings.Contains(plain, "Attached attached.txt") || strings.Contains(plain, path) {
		t.Fatalf("attachment path not displayed workspace-relative: %s", plain)
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
	if len(resumed.view.entries) != 2 || resumed.view.entries[1].blocks[0].Verb != "Attached" || resumed.view.entries[1].blocks[1].Verb != "Attach failed" || resumed.view.entries[1].blocks[0].Path != "attached.txt" || resumed.view.entries[1].blocks[1].Path != "missing.txt" {
		t.Fatal("resume did not preserve submitted outcomes")
	}
}

func TestLiveActivityAttachmentDisplayPaths(t *testing.T) {
	cwd := t.TempDir()
	inside := filepath.Join(cwd, "nested", "my `file`.txt")
	outside := filepath.Join(filepath.Dir(cwd), "outside.txt")
	for _, tt := range []struct {
		name, workspace, thread, path, want string
	}{
		{"inside", cwd, "main", inside, filepath.Join("nested", "my `file`.txt")},
		{"outside", cwd, "main", outside, outside},
		{"relative", cwd, "main", "relative.txt", "relative.txt"},
		{"unknown workspace", "", "main", inside, inside},
		{"child workspace not known", cwd, "child", inside, inside},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frame := "Attached file " + strconv.Quote(tt.path) + " (UTF-8 bytes 0:1 of 1; file content, not a separate request):\nx"
			content, err := json.Marshal([]map[string]string{{"type": "text", "text": encodeFileAttachments([]string{frame})}})
			if err != nil {
				t.Fatal(err)
			}
			v := newLiveActivityView()
			v.applyAppServerItem(tt.workspace, "main", tt.thread, "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
			if len(v.entries) != 2 || len(v.entries[1].blocks) != 1 || v.entries[1].blocks[0].Path != tt.want {
				t.Fatalf("attachment display path: got %+v, want %q", v.entries, tt.want)
			}
		})
	}
}

func TestLiveActivityAttachmentLookalikes(t *testing.T) {
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": "Attached file \"fake\" (UTF-8 bytes 0:1 of 1):\nx"}})
	if blocks := appServerAttachmentBlocks("", content); len(blocks) != 0 {
		t.Fatalf("ordinary text became attachment receipts: %+v", blocks)
	}
}

func TestLiveActivityAttachmentDialogs(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, body := range []string{"", "one", "one\n\nlast\n", "\x1b[2Jtab\tvalue\r\x07\n", strings.Repeat("界", fileAttachmentChunk)} {
			for _, skill := range []bool{false, true} {
				v := sharedEventsView(child)
				frames := frameComposerFile("/workspace/sample.go", body)
				if skill {
					frames = frameComposerSkillFromPath("guide", "/workspace/SKILL.md", body)
				}
				frames = append(frames, "Attached file \"/workspace/missing\": CONTENT NOT ATTACHED (\"missing\"). Read this separately if needed.")
				content, err := json.Marshal([]map[string]string{{"type": "text", "text": encodeFileAttachments(frames)}})
				if err != nil {
					t.Fatal(err)
				}
				thread := "main"
				if child {
					thread = "child"
				}
				item := appServerItem{Type: "userMessage", Content: content, ID: "input"}
				// The same host-owned item drives live delivery and fresh-process replay.
				for _, phase := range []string{"item/started", "item/completed"} {
					v.applyAppServerItem("/workspace", "main", thread, "turn", "input", phase, "", item)
				}
				if len(v.entries) != 2 || len(v.entries[1].blocks) != 2 {
					t.Fatalf("duplicate chunk or receipt: %+v", v.entries)
				}
				block := v.entries[1].blocks[0]
				if !block.Collapsed || !outputBlock(block) || outputBlock(v.entries[1].blocks[1]) {
					t.Fatal("receipt click eligibility is incorrect")
				}
				feed := v.renderFeed(80, 40)
				v.viewport(feed, 40)
				clicked := false
				for row, target := range v.feedSnippets {
					if target.run == 0 {
						continue
					}
					v.pointSnippet('\r', v.feedTop+row, v.feedLeft)
					u := &terminalUI{}
					if !u.openOutput(v, v.opening) {
						t.Fatal("attachment click did not open shared dialog")
					}
					page := v.painter.DialogPage(u.output.pages[0], 80)
					if page.Text != livediff.Safe(body, false) {
						t.Fatalf("dialog changed submitted content: got %d bytes, want %d", len(page.Text), len(body))
					}
					for i := range page.Lines {
						row := ansi.Strip(strings.Join(page.Rows(i, 80), "\n"))
						if strings.ContainsAny(row, "\x1b\t\r\x07") {
							t.Fatal("attachment terminal controls reached rendered dialog")
						}
					}
					if !v.following {
						t.Fatal("dialog changed follow state")
					}
					if !skill && len(page.Lines) > 0 && page.Lines[0].Gutter != "│" {
						t.Fatal("attachment did not use read source gutter")
					}
					clicked = true
					break
				}
				if !clicked {
					t.Fatal("successful receipt has no click target")
				}
				resumed, _ := newAppServerTestUI()
				resumed.agents = newLiveActivityView()
				resumed.session.start(resumed.thread, "/workspace")
				resumed.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
				page := resumed.view.painter.DialogPage(resumed.view.entries[1].blocks[0], 80)
				if page.Text != livediff.Safe(body, false) {
					t.Fatal("replay lost submitted content")
				}
			}
		}
	}
}

func TestUISnapshotAttachmentDialogs(t *testing.T) {
	v := sharedEventsView(false)
	frames := frameComposerFile("/workspace/sample.go", "package sample\n\nfunc Answer() int { return 42 }\n")
	frames = append(frames, frameComposerSkillFromPath("guide", "/workspace/SKILL.md", "# Guide\n\nUse **submitted** instructions.\n")...)
	frames = append(frames, "Attached file \"/workspace/missing.txt\": CONTENT NOT ATTACHED (\"missing\"). Read this separately if needed.")
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": encodeFileAttachments(frames)}})
	v.applyAppServerItem("/workspace", "main", "main", "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
	feed := v.renderFeed(80, 40)
	uisnapshot.Assert(t, "testdata/snapshots/attachment-receipts.txt", strings.Join(feed.lines, "\n")+"\n")
	for i, name := range []string{"file", "skill"} {
		page := v.painter.DialogPage(v.entries[1].blocks[i], 72)
		var rows []string
		for line := range page.Lines {
			rows = append(rows, page.Rows(line, 72)...)
		}
		frame := activityui.DialogFrame{Page: page, Rows: rows, Total: len(rows), Footer: "escape close · scroll"}
		uisnapshot.Assert(t, "testdata/snapshots/attachment-dialog-"+name+".txt", strings.Join(v.painter.Dialog(frame, 80, 14), "\n")+"\n")
	}
}

func TestLiveActivityAttachmentStackedSnapshots(t *testing.T) {
	for _, skill := range []bool{false, true} {
		for _, second := range []string{"first\n", "second\n"} {
			var drafts []composerDraft
			for _, body := range []string{"first\n", second} {
				frames := frameComposerFile("/workspace/sample.txt", body)
				if skill {
					frames = frameComposerSkillFromPath("guide", "/workspace/SKILL.md", body)
				}
				drafts = append(drafts, composerDraft{text: "review", attachments: []string{encodeFileAttachments(frames)}})
			}
			joined := joinDrafts(drafts...)
			content, err := json.Marshal(joined.input())
			if err != nil {
				t.Fatal(err)
			}
			v := sharedEventsView(false)
			v.applyAppServerItem("/workspace", "main", "main", "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
			blocks := v.entries[1].blocks
			if len(blocks) != 2 {
				t.Fatalf("stacked snapshots collapsed together: %d receipts", len(blocks))
			}
			for i, want := range []string{"first\n", second} {
				page := v.painter.DialogPage(blocks[i], 80)
				if page.Text != want {
					t.Fatalf("snapshot %d: got %q, want %q", i, page.Text, want)
				}
			}
		}
	}
}
