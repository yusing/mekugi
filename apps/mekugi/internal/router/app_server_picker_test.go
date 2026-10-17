package router

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type pickerWireRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		Query string   `json:"query"`
		Roots []string `json:"roots"`
		Cwds  []string `json:"cwds"`
	} `json:"params"`
}

func pickerRequests(t *testing.T, w *appServerTestInput) []pickerWireRequest {
	t.Helper()
	var requests []pickerWireRequest
	for line := range bytes.SplitSeq(bytes.TrimSpace(w.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var request pickerWireRequest
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	return requests
}

func pickerReply(t *testing.T, u *appServerUI, id int, result string) {
	t.Helper()
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, id, result))
}

func TestComposerFilePickerScopesAndCoalescesRequests(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work/one"
	appServerTestKeys(t, u, "@ab")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "fuzzyFileSearch" || requests[0].Params.Query != "a" || len(requests[0].Params.Roots) != 1 || requests[0].Params.Roots[0] != "/work/one" {
		t.Fatalf("initial search = %+v", requests)
	}
	if !u.picker.Loading || u.picker.Target.Query != "ab" {
		t.Fatalf("typing did not coalesce: %+v", u.picker)
	}
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work/one","path":"stale.go"}]}`)
	requests = pickerRequests(t, w)
	if len(requests) != 2 || requests[1].Params.Query != "ab" || len(u.picker.Choices) != 0 {
		t.Fatalf("stale response was shown or latest search missing: %+v, %+v", requests, u.picker.Choices)
	}
	pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work/other","path":"leak.go"},{"root":"/work/one","path":"ab.go"}]}`)
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Path != "ab.go" {
		t.Fatalf("unscoped search results: %+v", u.picker.Choices)
	}
	u.attached.cwd = "/work/two"
	u.refreshPicker()
	requests = pickerRequests(t, w)
	if len(requests) != 3 || len(requests[2].Params.Roots) != 1 || requests[2].Params.Roots[0] != "/work/two" {
		t.Fatalf("workspace change did not request fresh search: %+v", requests)
	}
}

func TestComposerSkillPickerFiltersAndSubmitsAttachedContents(t *testing.T) {
	u, w := newAppServerTestUI()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "quiet", "SKILL.md")
	const instructions = "Picker skill instructions 世界.\n"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(instructions), 0600); err != nil {
		t.Fatal(err)
	}
	u.attached.cwd = workspace
	appServerTestKeys(t, u, "$qu")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" || len(requests[0].Params.Cwds) != 1 || requests[0].Params.Cwds[0] != workspace {
		t.Fatalf("skill list scope = %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, fmt.Sprintf(`{"data":[{"cwd":"/other","skills":[{"name":"quiet","path":"/other/SKILL.md","enabled":true}]},{"cwd":%q,"skills":[{"name":"quiet","path":%q,"enabled":true,"interface":{"shortDescription":"Short help"}},{"name":"queue","path":"/work/queue/SKILL.md","enabled":false},{"name":"build","path":"/work/build/SKILL.md","enabled":true}]}]}`, workspace, path))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Name != "quiet" || u.picker.Choices[0].Description != "Short help" {
		t.Fatalf("skill filtering = %+v", u.picker.Choices)
	}
	appServerTestKeys(t, u, "\t")
	if u.Text != "$quiet " || len(u.Skills) != 1 || u.Skills[0].Path != path || len(u.attached.source.Entries) != 0 {
		t.Fatalf("skill insertion submitted or lost binding: draft=%q skills=%+v entries=%d", u.Text, u.Skills, len(u.attached.source.Entries))
	}
	w.Reset()
	appServerTestKeys(t, u, "\r")
	var request struct {
		Params struct {
			Input []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Params.Input) != 2 || request.Params.Input[1].Type != "text" {
		t.Fatalf("skill contents were not sent in a text attachment: %+v", request.Params.Input)
	}
	frames, ok := attachments.Decode(request.Params.Input[1].Text)
	if !ok || len(frames) != 1 || frames[0] != composer.SkillFrames("quiet", path, instructions)[0] {
		t.Fatalf("skill contents or source path missing: %+v", frames)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.Text != "$quiet " || len(u.Skills) != 1 || u.Skills[0].Path != path {
		t.Fatalf("rejected submission lost skill: draft=%q skills=%+v", u.Text, u.Skills)
	}
}

func TestComposerFilePickerInsertionUndoRedoAndPasteOpacity(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "see @fi")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"files":[]}`)
	requests = pickerRequests(t, w)
	pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work","path":"file name.go"}]}`)
	appServerTestKeys(t, u, "\t")
	if u.Text != `see @"file name.go" ` || len(u.attached.source.Entries) != 0 {
		t.Fatalf("file insertion submitted or did not quote whitespace: %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "see @fi" {
		t.Fatalf("undo picker replacement = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x19")
	if u.Text != `see @"file name.go" ` {
		t.Fatalf("redo picker replacement = %q", u.Text)
	}
	v, input := newAppServerTestUI()
	v.attached.cwd = "/work"
	appServerTestKeys(t, v, "\x1b[200~@secret $quiet\x1b[201~")
	if v.Text != "@secret $quiet" || v.picker.Open || input.Len() != 0 {
		t.Fatalf("paste opened search: draft=%q picker=%+v", v.Text, v.picker)
	}
}

func TestComposerPickerEscapeViaShellAndFrameBounds(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"

	for _, key := range []byte("@file") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	if !u.picker.Open {
		t.Fatal("picker did not open")
	}
	if err := terminalTest(u).key(27); err != nil {
		t.Fatal(err)
	}
	ageTerminalEscape(terminalTest(u))
	if err := terminalTest(u).flushEscape(); err != nil {
		t.Fatal(err)
	}
	if u.picker.Open || u.Text != "@file" {
		t.Fatalf("escape altered draft or left picker open: %q %+v", u.Text, u.picker)
	}
	u.refreshPicker()
	if u.picker.Open {
		t.Fatal("dismissed picker reopened without an edit")
	}
	for _, size := range [][2]int{{1, 1}, {12, 5}, {80, 24}} {
		frame, _ := u.testMainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("size %v: %d rows", size, len(frame))
		}
		for _, row := range frame {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("size %v: overflowing row %q", size, row)
			}
		}
	}
	if len(u.attached.source.Entries) != 0 || strings.Contains(w.String(), "turn/start") {
		t.Fatal("picker activity polluted transcript or submitted a turn")
	}
}

func TestComposerSkillPickerUndoHistoryAndAtomicEditing(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "/skills\r1")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}]}]}`)
	appServerTestKeys(t, u, "\r")
	if u.Text != "$review " || len(u.Skills) != 1 {
		t.Fatalf("selection = %q %+v", u.Text, u.Skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "$" || len(u.Skills) != 0 {
		t.Fatalf("undo = %q %+v", u.Text, u.Skills)
	}
	appServerTestKeys(t, u, "\x19")
	if u.Text != "$review " || len(u.Skills) != 1 {
		t.Fatalf("redo = %q %+v", u.Text, u.Skills)
	}
	appServerTestKeys(t, u, "\x7f\x7f")
	if u.Text != "" || len(u.Skills) != 0 {
		t.Fatalf("atomic deletion = %q %+v", u.Text, u.Skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.Skills) != 1 {
		t.Fatal("undo deletion lost skill")
	}
	appServerTestKeys(t, u, "\r")
	requests = pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"turn":{"id":"turn"}}`)
	u.recallInput(true)
	if len(u.Skills) != 1 || u.Skills[0].Path != "/work/review/SKILL.md" {
		t.Fatalf("history = %+v", u.Skills)
	}
}

func TestComposerSkillPickerResumeAndEditorBindings(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/work")

	var turns []binding.HistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"turn","status":"completed","items":[{"type":"userMessage","id":"message","content":[{"type":"text","text":"use $review please"},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	u.recallInput(true)
	if len(u.Skills) != 1 || u.Skills[0].Start != 4 {
		t.Fatalf("restored skills = %+v", u.Skills)
	}
	if err := u.applyEditorDraft(editorTestIntent(u), "now use $review please"); err != nil {
		t.Fatal(err)
	}
	if len(u.Skills) != 1 || u.Skills[0].Start != 8 {
		t.Fatalf("editor skills = %+v", u.Skills)
	}
	if err := u.applyEditorDraft(editorTestIntent(u), "$review-other"); err != nil {
		t.Fatal(err)
	}
	if len(u.Skills) != 0 {
		t.Fatal("editor retained binding for a different word")
	}
}

func TestComposerPickerFailureRecoveryAndRendering(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "@a")
	request := pickerRequests(t, w)[0]
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"offline"}}`, request.ID))
	rows, _ := u.testMainFrame(80, 20, 5)
	if !strings.Contains(strings.Join(rows, "\n"), "Search failed: offline") || u.picker.Loading {
		t.Fatal("missing error feedback")
	}
	if len(pickerRequests(t, w)) != 1 {
		t.Fatal("failure retried without input")
	}
	appServerTestKeys(t, u, "b")
	requests := pickerRequests(t, w)
	if len(requests) != 2 || requests[1].Params.Query != "ab" {
		t.Fatal("editing did not retry")
	}
	pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work","path":"some/deep/path/ab.go"}]}`)
	for _, size := range [][2]int{{1, 1}, {12, 5}, {80, 24}} {
		frame, dock := u.testMainFrame(size[0], size[1], 5)
		if dock.H != 0 || len(frame) != size[1] {
			t.Fatalf("bad picker geometry %v: dock=%+v rows=%d", size, dock, len(frame))
		}
		for _, line := range frame {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("overflow %v: %q", size, line)
			}
		}
	}
}

func TestComposerPickerScore(t *testing.T) {
	for _, test := range []struct {
		name, query string
		score       int
		ok          bool
	}{
		{"hello", "hl", -99, true}, {"İstanbul", "is", -99, true}, {"a-b-c", "abc", -98, true},
		{"abc", "abc", -100, true}, {"my_file", "file", 0, true}, {"straße", "strasse", 0, false},
	} {
		score, ok := completion.MatchScore(test.name, test.query)
		if score != test.score || ok != test.ok {
			t.Fatalf("%q %q = %d,%v", test.name, test.query, score, ok)
		}
	}
}

func TestComposerFilePickerImageAndUndoCapacity(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attached.cwd = t.TempDir()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(u.attached.cwd, "shot.png")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for range 110 {
		u.insertDraft("word ")
		u.Run = composer.NoRun
	}
	u.insertDraft("@s")
	target := u.completionTarget()
	u.attached.conversation.Completion = completion.State{Open: true, Target: target, Choices: []completion.Choice{{Name: "shot.png", Path: "shot.png"}}}
	before := u.Text
	if !u.pickerKey("\t") || len(u.Images) != 1 || u.Images[0].Path != path {
		t.Fatalf("image choice: %q %+v", u.Text, u.Images)
	}
	u.undoDraft(false)
	if u.Text != before || len(u.Images) != 0 {
		t.Fatalf("undo at capacity = %q %+v", u.Text, u.Images)
	}
	u.undoDraft(true)
	if len(u.Images) != 1 {
		t.Fatal("redo lost image")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("picker removed user image", err)
	}
}

func TestComposerPickerEditorRefreshBeforeSelection(t *testing.T) {
	for _, key := range []string{"\t", "\r"} {
		u, _ := newAppServerTestUI()
		u.attached.cwd = "/work"
		u.Text = "long prompt @file"
		u.refreshPicker()
		target := u.completionTarget()
		u.attached.conversation.Completion = completion.State{Open: true, Target: target, Resolved: target, ResolvedCwd: "/work", Choices: []completion.Choice{{Name: "file.go", Path: "file.go"}}}
		if err := u.applyEditorDraft(editorTestIntent(u), "new"); err != nil {
			t.Fatal(err)
		}
		if u.picker.Open {
			t.Fatal("editor left stale picker visible")
		}
		appServerTestKeys(t, u, key)
		if len(u.Images) != 0 || len(u.Skills) != 0 || strings.Contains(u.Text, "file.go") {
			t.Fatal("stale selection changed edited draft")
		}
		if key == "\r" && u.attached.conversation.Submitted[u.Submission().ID].Text != "new" {
			t.Fatalf("edited submission = %q", u.attached.conversation.Submitted[u.Submission().ID].Text)
		}
	}
}

func TestComposerPickerDoesNotMarkCoveredTranscriptPainted(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "@f")
	for _, height := range []int{6, 24} {
		frame, _ := u.testMainFrame(80, height, 0)
		if !strings.Contains(strings.Join(frame, "\n"), "loading...") || u.shell.Main.ContentPainted {
			t.Fatalf("height %d: covered transcript acknowledged", height)
		}
	}
	u.pickerKey("\x1b")
	u.testMainFrame(80, 24, 0)
	if !u.shell.Main.ContentPainted {
		t.Fatal("dismissal did not restore transcript presentation")
	}
}

// These fixtures publish the source transition before consuming terminal state.
func (u *appServerUI) pickerKey(key string) bool {
	u.settlePresentation()
	return u.applyMenuResult(u.shell.Main.Menu.Key(key))
}
func (u *appServerUI) pickerHeight(width int) int {
	u.settlePresentation()
	return u.shell.Main.Menu.Height(width)
}
func (u *appServerUI) renderPicker(width, height int) []string {
	u.settlePresentation()
	return u.shell.Main.Menu.Render(&u.shell.Transcript.Painter.Painter, terminalsession.SelectionStyle(&u.shell.Transcript.Painter.Painter), width, height)
}

func TestComposerCompletionAttachmentLifetime(t *testing.T) {
	u, _ := newAppServerTestUI()
	previous := u.attached.conversation
	u.picker.Catalog([]completion.Choice{{Name: "skill", Path: "/work/SKILL.md"}}, "/work", true, "")
	u.picker.CopyTexts = []string{"retained response"}
	u.attachPresentation()
	if !u.attached.conversation.Completion.SkillsLoaded || len(u.attached.conversation.Completion.CopyTexts) != 1 {
		t.Fatal("attachment binding discarded Conversation completion values")
	}
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	if u.attached.conversation == previous || u.picker.State != &u.attached.conversation.Completion || u.picker.SkillsLoaded || len(u.picker.CopyTexts) != 0 {
		t.Fatal("replacement retained old completion or copy choices")
	}
}
