package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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
	u.session.cwd = "/work/one"
	appServerTestKeys(t, u, "@ab")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "fuzzyFileSearch" || requests[0].Params.Query != "a" || len(requests[0].Params.Roots) != 1 || requests[0].Params.Roots[0] != "/work/one" {
		t.Fatalf("initial search = %+v", requests)
	}
	if !u.picker.loading || u.picker.target.query != "ab" {
		t.Fatalf("typing did not coalesce: %+v", u.picker)
	}
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work/one","path":"stale.go"}]}`)
	requests = pickerRequests(t, w)
	if len(requests) != 2 || requests[1].Params.Query != "ab" || len(u.picker.choices) != 0 {
		t.Fatalf("stale response was shown or latest search missing: %+v, %+v", requests, u.picker.choices)
	}
	pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work/other","path":"leak.go"},{"root":"/work/one","path":"ab.go"}]}`)
	if len(u.picker.choices) != 1 || u.picker.choices[0].path != "ab.go" {
		t.Fatalf("unscoped search results: %+v", u.picker.choices)
	}
	u.session.cwd = "/work/two"
	u.refreshPicker()
	requests = pickerRequests(t, w)
	if len(requests) != 3 || len(requests[2].Params.Roots) != 1 || requests[2].Params.Roots[0] != "/work/two" {
		t.Fatalf("workspace change did not request fresh search: %+v", requests)
	}
}

func TestComposerSkillPickerFiltersAndSubmitsStructuredInput(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "$qu")
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" || len(requests[0].Params.Cwds) != 1 || requests[0].Params.Cwds[0] != "/work" {
		t.Fatalf("skill list scope = %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/other","skills":[{"name":"quiet","path":"/other/SKILL.md","enabled":true}]},{"cwd":"/work","skills":[{"name":"quiet","path":"/work/quiet/SKILL.md","enabled":true,"interface":{"shortDescription":"Short help"}},{"name":"queue","path":"/work/queue/SKILL.md","enabled":false},{"name":"build","path":"/work/build/SKILL.md","enabled":true}]}]}`)
	if len(u.picker.choices) != 1 || u.picker.choices[0].name != "quiet" || u.picker.choices[0].description != "Short help" {
		t.Fatalf("skill filtering = %+v", u.picker.choices)
	}
	appServerTestKeys(t, u, "\t")
	if u.draft != "$quiet " || len(u.skills) != 1 || u.skills[0].path != "/work/quiet/SKILL.md" || len(u.view.entries) != 0 {
		t.Fatalf("skill insertion submitted or lost binding: draft=%q skills=%+v entries=%d", u.draft, u.skills, len(u.view.entries))
	}
	w.Reset()
	appServerTestKeys(t, u, "\r")
	var request struct {
		Params struct {
			Input []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Params.Input) != 2 || request.Params.Input[1].Type != "skill" || request.Params.Input[1].Name != "quiet" || request.Params.Input[1].Path != "/work/quiet/SKILL.md" {
		t.Fatalf("structured skill input = %+v", request.Params.Input)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.draft != "$quiet " || len(u.skills) != 1 || u.skills[0].path != "/work/quiet/SKILL.md" {
		t.Fatalf("rejected submission lost skill: draft=%q skills=%+v", u.draft, u.skills)
	}
}

func TestComposerFilePickerInsertionUndoRedoAndPasteOpacity(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "see @fi")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"files":[]}`)
	requests = pickerRequests(t, w)
	pickerReply(t, u, requests[1].ID, `{"files":[{"root":"/work","path":"file name.go"}]}`)
	appServerTestKeys(t, u, "\t")
	if u.draft != `see @"file name.go" ` || len(u.view.entries) != 0 {
		t.Fatalf("file insertion submitted or did not quote whitespace: %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "see @fi" {
		t.Fatalf("undo picker replacement = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x19")
	if u.draft != `see @"file name.go" ` {
		t.Fatalf("redo picker replacement = %q", u.draft)
	}
	v, input := newAppServerTestUI()
	v.session.cwd = "/work"
	appServerTestKeys(t, v, "\x1b[200~@secret $quiet\x1b[201~")
	if v.draft != "@secret $quiet" || v.picker.open || input.Len() != 0 {
		t.Fatalf("paste opened search: draft=%q picker=%+v", v.draft, v.picker)
	}
}

func TestComposerPickerEscapeViaShellAndFrameBounds(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	u.ensureShell()
	for _, key := range []byte("@file") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if !u.picker.open {
		t.Fatal("picker did not open")
	}
	if err := u.shell.key(27); err != nil {
		t.Fatal(err)
	}
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	if err := u.shell.flushEscape(); err != nil {
		t.Fatal(err)
	}
	if u.picker.open || u.draft != "@file" {
		t.Fatalf("escape altered draft or left picker open: %q %+v", u.draft, u.picker)
	}
	u.refreshPicker()
	if u.picker.open {
		t.Fatal("dismissed picker reopened without an edit")
	}
	for _, size := range [][2]int{{1, 1}, {12, 5}, {80, 24}} {
		frame, _ := u.mainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("size %v: %d rows", size, len(frame))
		}
		for _, row := range frame {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("size %v: overflowing row %q", size, row)
			}
		}
	}
	if len(u.view.entries) != 0 || strings.Contains(w.String(), "turn/start") {
		t.Fatal("picker activity polluted transcript or submitted a turn")
	}
}

func TestComposerSkillPickerUndoHistoryAndAtomicEditing(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "/skills\r1")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":"/work","skills":[{"name":"review","path":"/work/review/SKILL.md","enabled":true}]}]}`)
	appServerTestKeys(t, u, "\r")
	if u.draft != "$review " || len(u.skills) != 1 {
		t.Fatalf("selection = %q %+v", u.draft, u.skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "$" || len(u.skills) != 0 {
		t.Fatalf("undo = %q %+v", u.draft, u.skills)
	}
	appServerTestKeys(t, u, "\x19")
	if u.draft != "$review " || len(u.skills) != 1 {
		t.Fatalf("redo = %q %+v", u.draft, u.skills)
	}
	appServerTestKeys(t, u, "\x7f\x7f")
	if u.draft != "" || len(u.skills) != 0 {
		t.Fatalf("atomic deletion = %q %+v", u.draft, u.skills)
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.skills) != 1 {
		t.Fatal("undo deletion lost skill")
	}
	appServerTestKeys(t, u, "\r")
	requests = pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"turn":{"id":"turn"}}`)
	u.recallInput(true)
	if len(u.skills) != 1 || u.skills[0].path != "/work/review/SKILL.md" {
		t.Fatalf("history = %+v", u.skills)
	}
}

func TestComposerSkillPickerResumeAndEditorBindings(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.session.start("main", "/work")
	u.agents = newLiveActivityView()
	var turns []appServerHistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"turn","status":"completed","items":[{"type":"userMessage","id":"message","content":[{"type":"text","text":"use $review please"},{"type":"skill","name":"review","path":"/work/review/SKILL.md"}]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	u.recallInput(true)
	if len(u.skills) != 1 || u.skills[0].start != 4 {
		t.Fatalf("restored skills = %+v", u.skills)
	}
	if err := u.applyEditorDraft("now use $review please"); err != nil {
		t.Fatal(err)
	}
	if len(u.skills) != 1 || u.skills[0].start != 8 {
		t.Fatalf("editor skills = %+v", u.skills)
	}
	if err := u.applyEditorDraft("$review-other"); err != nil {
		t.Fatal(err)
	}
	if len(u.skills) != 0 {
		t.Fatal("editor retained binding for a different word")
	}
}

func TestComposerPickerFailureRecoveryAndRendering(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "@a")
	request := pickerRequests(t, w)[0]
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"offline"}}`, request.ID))
	rows, _ := u.mainFrame(80, 20, 5)
	if !strings.Contains(strings.Join(rows, "\n"), "Search failed: offline") || u.picker.loading {
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
		frame, dock := u.mainFrame(size[0], size[1], 5)
		if dock.h != 0 || len(frame) != size[1] {
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
		score, ok := pickerMatchScore(test.name, test.query)
		if score != test.score || ok != test.ok {
			t.Fatalf("%q %q = %d,%v", test.name, test.query, score, ok)
		}
	}
}

func TestComposerFilePickerImageAndUndoCapacity(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.session.cwd = t.TempDir()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(u.session.cwd, "shot.png")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for range 110 {
		u.insertDraft("word ")
		u.run = runNone
	}
	u.insertDraft("@s")
	target := u.completionTarget()
	u.picker = composerPicker{open: true, target: target, choices: []composerChoice{{name: "shot.png", path: "shot.png"}}}
	before := u.draft
	if !u.pickerKey("\t") || len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("image choice: %q %+v", u.draft, u.images)
	}
	u.undoDraft(false)
	if u.draft != before || len(u.images) != 0 {
		t.Fatalf("undo at capacity = %q %+v", u.draft, u.images)
	}
	u.undoDraft(true)
	if len(u.images) != 1 {
		t.Fatal("redo lost image")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("picker removed user image", err)
	}
}

func TestComposerPickerEditorRefreshBeforeSelection(t *testing.T) {
	for _, key := range []string{"\t", "\r"} {
		u, _ := newAppServerTestUI()
		u.session.cwd = "/work"
		u.draft = "long prompt @file"
		u.refreshPicker()
		target := u.completionTarget()
		u.picker = composerPicker{open: true, target: target, resolved: target, resolvedCwd: "/work", choices: []composerChoice{{name: "file.go", path: "file.go"}}}
		if err := u.applyEditorDraft("new"); err != nil {
			t.Fatal(err)
		}
		if u.picker.open {
			t.Fatal("editor left stale picker visible")
		}
		appServerTestKeys(t, u, key)
		if len(u.images) != 0 || len(u.skills) != 0 || strings.Contains(u.draft, "file.go") {
			t.Fatal("stale selection changed edited draft")
		}
		if key == "\r" && u.submission.text != "new" {
			t.Fatalf("edited submission = %q", u.submission.text)
		}
	}
}

func TestComposerPickerDoesNotAcknowledgeCoveredJournal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "@f")
	for _, height := range []int{6, 24} {
		frame, _ := u.mainFrame(80, height, 0)
		if !strings.Contains(strings.Join(frame, "\n"), "loading...") || u.mainContentPainted {
			t.Fatalf("height %d: covered transcript acknowledged", height)
		}
	}
	u.pickerKey("\x1b")
	u.mainFrame(80, 24, 0)
	if !u.mainContentPainted {
		t.Fatal("dismissal did not restore journal presentation")
	}
}
