package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestLiveActivityAttachmentOutcomes(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "attached.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("private file contents\n", 1500)), 0600); err != nil {
		t.Fatal(err)
	}
	draft := conversationmodel.Input{Text: "Review files", Files: []composer.Token{{Path: path}, {Path: filepath.Join(cwd, "missing.txt")}}}
	draft.SnapshotFiles(cwd)
	content, err := json.Marshal(conversationbinding.Input(draft.Draft))
	if err != nil {
		t.Fatal(err)
	}
	item := appServerItem{Type: "userMessage", Content: content, ID: "input"}
	u, _ := newAppServerTestUI()
	for _, method := range []string{"item/started", "item/completed", "item/completed"} {
		applyTranscriptFixture(u.view, true, cwd, u.Thread, u.Thread, "turn", item.ID, method, "", item)
	}
	applyTranscriptFixture(u.view, true, cwd, u.Thread, u.Thread, "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Agent response"})
	if len(u.view.Entries) != 3 || u.view.Entries[1].Source.Kind != "attachments" {
		t.Fatalf("missing, duplicated, or reordered receipts: %+v", u.view.Entries)
	}
	blocks := u.view.blocks(1)
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
	if attached < 0 || failed < attached || response < failed || strings.Contains(plain, "private file contents") || strings.Contains(plain, attachments.Prefix) {
		t.Fatalf("incorrect receipt rendering/order: %s", plain)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	resumed, _ := newAppServerTestUI()
	resumed.agents = newActivityAdapter()
	resumed.session.start(resumed.Thread, cwd)
	resumed.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
	if len(resumed.view.Entries) != 2 || resumed.view.blocks(1)[0].Verb != "Attached" || resumed.view.blocks(1)[1].Verb != "Attach failed" || resumed.view.blocks(1)[0].Path != "attached.txt" || resumed.view.blocks(1)[1].Path != "missing.txt" {
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
			content, err := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode([]string{frame})}})
			if err != nil {
				t.Fatal(err)
			}
			v := newActivityAdapter()
			applyTranscriptFixture(v, true, tt.workspace, "main", tt.thread, "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
			if len(v.Entries) != 2 || len(v.blocks(1)) != 1 || v.blocks(1)[0].Path != tt.want {
				t.Fatalf("attachment display path: got %+v, want %q", v.Entries, tt.want)
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

func TestUISnapshotExpandedSkillMatchesDialog(t *testing.T) {
	const body = "# Commit\n\nA documented project convention (**contributing guide**, `commitlint` or similar config) takes precedence over the message format below.\n\n## Shape commits\n\n- Make each commit one coherent, independently valid change.\n\n```sh\ngit status --short\n```"
	for _, child := range []bool{false, true} {
		v := sharedEventsView(child)
		content, err := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode(composer.SkillFrames("commit", "/workspace/SKILL.md", body))}})
		if err != nil {
			t.Fatal(err)
		}
		thread := "main"
		if child {
			thread = "child"
		}
		applyTranscriptFixture(v, true, "/workspace", "main", thread, "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content, ID: "input"})
		v.ToggleExpansion()
		v.ToggleExpansion()
		feed := v.RenderFeed(70, 40)
		v.Viewport(feed, 40)
		block := v.blocks(1)[0]
		page := v.Painter.DialogPage(block, 70)
		if page.Text != body {
			t.Fatal("expanded skill changed its dialog copy source")
		}
		name := "main"
		if child {
			name = "activity"
		}
		uisnapshot.AssertTerminal(t, "testdata/snapshots/expanded-skill-"+name+".txt", append(feed.Lines, "plain after skill"), 70)
	}
}

func TestLiveActivityAttachmentDialogs(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		for _, body := range []string{"", "one", "one\n\nlast\n", "\x1b[2Jtab\tvalue\r\x07\n", strings.Repeat("界", attachments.Chunk)} {
			for _, skill := range []bool{false, true} {
				v := sharedEventsView(child)
				frames := composer.FileFrames("/workspace/sample.go", body)
				if skill {
					frames = composer.SkillFrames("guide", "/workspace/SKILL.md", body)
				}
				frames = append(frames, "Attached file \"/workspace/missing\": CONTENT NOT ATTACHED (\"missing\"). Read this separately if needed.")
				content, err := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode(frames)}})
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
					applyTranscriptFixture(v, true, "/workspace", "main", thread, "turn", "input", phase, "", item)
				}
				if len(v.Entries) != 2 || len(v.blocks(1)) != 2 {
					t.Fatalf("duplicate chunk or receipt: %+v", v.Entries)
				}
				block := v.blocks(1)[0]
				if !block.Collapsed || !activityui.OutputBlock(block) || activityui.OutputBlock(v.blocks(1)[1]) {
					t.Fatal("receipt click eligibility is incorrect")
				}
				feed := v.RenderFeed(80, 40)
				v.Viewport(feed, 40)
				clicked := false
				for row, target := range v.FeedSnippets {
					if target.Run == 0 {
						continue
					}
					v.PointSnippet('\r', v.FeedTop+row, v.FeedLeft)
					u := &terminalUI{}
					if !u.openOutput(v, v.Opening) {
						t.Fatal("attachment click did not open shared dialog")
					}
					page := v.Painter.DetailDocument(u.output.pages[0].DetailPage, nil, nil, 80)
					if page.Text != textsanitize.Safe(body, false) {
						t.Fatalf("dialog changed submitted content: got %d bytes, want %d", len(page.Text), len(body))
					}
					for i := range page.Lines {
						row := ansi.Strip(strings.Join(page.Rows(i, 80), "\n"))
						if strings.ContainsAny(row, "\x1b\t\r\x07") {
							t.Fatal("attachment terminal controls reached rendered dialog")
						}
					}
					if !v.Following {
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
				resumed.agents = newActivityAdapter()
				resumed.session.start(resumed.Thread, "/workspace")
				resumed.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
				page := resumed.view.Painter.DialogPage(resumed.view.blocks(1)[0], 80)
				if page.Text != textsanitize.Safe(body, false) {
					t.Fatal("replay lost submitted content")
				}
			}
		}
	}
}

func TestUISnapshotAttachmentDialogs(t *testing.T) {
	v := sharedEventsView(false)
	frames := composer.FileFrames("/workspace/sample.go", "package sample\n\nfunc Answer() int { return 42 }\n")
	frames = append(frames, composer.SkillFrames("guide", "/workspace/SKILL.md", "# Guide\n\nUse **submitted** instructions.\n")...)
	frames = append(frames, "Attached file \"/workspace/missing.txt\": CONTENT NOT ATTACHED (\"missing\"). Read this separately if needed.")
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode(frames)}})
	applyTranscriptFixture(v, true, "/workspace", "main", "main", "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
	feed := v.RenderFeed(80, 40)
	uisnapshot.Assert(t, "testdata/snapshots/attachment-receipts.txt", strings.Join(feed.Lines, "\n")+"\n")
	for i, name := range []string{"file", "skill"} {
		page := v.Painter.DialogPage(v.blocks(1)[i], 72)
		var rows []string
		for line := range page.Lines {
			rows = append(rows, page.Rows(line, 72)...)
		}
		frame := terminalui.DialogFrame{Page: page, Rows: rows, Total: len(rows), Footer: "escape close · scroll"}
		uisnapshot.Assert(t, "testdata/snapshots/attachment-dialog-"+name+".txt", strings.Join(v.Painter.Dialog(frame, 80, 14), "\n")+"\n")
	}
}

func TestUISnapshotDirectoryAttachment(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, "src")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", "nested/child.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("contents must not appear"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	draft := conversationmodel.Input{Text: "Review @src", Files: []composer.Token{{Path: "src"}}}
	draft.SnapshotFiles(cwd)
	content, _ := json.Marshal(conversationbinding.Input(draft.Draft))
	v := sharedEventsView(false)
	applyTranscriptFixture(v, true, cwd, "main", "main", "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
	feed := v.RenderFeed(80, 40)
	uisnapshot.Assert(t, "testdata/snapshots/directory-attachment-receipt.txt", strings.Join(feed.Lines, "\n")+"\n")
	page := v.Painter.DialogPage(v.blocks(1)[0], 72)
	var rows []string
	for line := range page.Lines {
		rows = append(rows, page.Rows(line, 72)...)
	}
	frame := terminalui.DialogFrame{Page: page, Rows: rows, Total: len(rows), Footer: "escape close · scroll"}
	uisnapshot.Assert(t, "testdata/snapshots/directory-attachment-dialog.txt", strings.Join(v.Painter.Dialog(frame, 80, 14), "\n")+"\n")
}

func TestLiveActivityAttachmentStackedSnapshots(t *testing.T) {
	for _, skill := range []bool{false, true} {
		for _, second := range []string{"first\n", "second\n"} {
			var drafts []conversationmodel.Input
			for _, body := range []string{"first\n", second} {
				frames := composer.FileFrames("/workspace/sample.txt", body)
				if skill {
					frames = composer.SkillFrames("guide", "/workspace/SKILL.md", body)
				}
				drafts = append(drafts, conversationmodel.Input{Text: "review", Attachments: []string{attachments.Encode(frames)}})
			}
			joined := conversationmodel.Join(drafts...)
			content, err := json.Marshal(conversationbinding.Input(joined.Draft))
			if err != nil {
				t.Fatal(err)
			}
			v := sharedEventsView(false)
			applyTranscriptFixture(v, true, "/workspace", "main", "main", "turn", "input", "item/completed", "", appServerItem{Type: "userMessage", Content: content})
			blocks := v.blocks(1)
			if len(blocks) != 2 {
				t.Fatalf("stacked snapshots collapsed together: %d receipts", len(blocks))
			}
			for i, want := range []string{"first\n", second} {
				page := v.Painter.DialogPage(blocks[i], 80)
				if page.Text != want {
					t.Fatalf("snapshot %d: got %q, want %q", i, page.Text, want)
				}
			}
		}
	}
}
