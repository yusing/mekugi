package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComposerFilePickerExcludedMode(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{".git/objects/excluded-object", ".svn/excluded-entry", ".hg/store/excluded-data", "node_modules/excluded-module.js", "visible.txt", ".gitignore"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("node_modules/\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	choices, problem := scanPickerFiles(t.Context(), root, "excluded")
	if problem != "" {
		t.Fatal(problem)
	}
	if len(choices) != 1 || choices[0].path != "node_modules/excluded-module.js" {
		t.Fatalf("@! should include ignored files, not VCS metadata: %+v", choices)
	}
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.cwd = root
	t.Cleanup(u.cancelPickerScan)
	appServerTestKeys(t, u, "@!excluded")
	if len(pickerRequests(t, w)) != 0 {
		t.Fatal("@! sent an ignore-respecting host search")
	}
	select {
	case result := <-u.picker.scanResults:
		u.applyPickerScan(result)
	case <-time.After(5 * time.Second):
		t.Fatal("excluded search did not finish")
	}
	if len(u.picker.choices) != 1 || u.picker.loading {
		t.Fatalf("excluded picker = %+v", u.picker)
	}
	appServerTestKeys(t, u, "\t")
	if strings.Contains(u.draft, "@!") || !strings.Contains(u.draft, "excluded") {
		t.Fatalf("modifier leaked into selection: %q", u.draft)
	}
}

func TestComposerFilePickerNormalHidesGitMetadata(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "@g")
	request := pickerRequests(t, w)[0]
	pickerReply(t, u, request.ID, `{"files":[{"root":"/work","path":".git","match_type":"directory"},{"root":"/work","path":".git/config"},{"root":"/work","path":"nested/.git/HEAD"},{"root":"/work","path":"git.go"}]}`)
	if len(u.picker.choices) != 1 || u.picker.choices[0].path != "git.go" {
		t.Fatalf("normal @ exposes git metadata: %+v", u.picker.choices)
	}
}

func TestComposerFilePickerCanceledResultsCannotReopen(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.cwd = t.TempDir()
	t.Cleanup(u.cancelPickerScan)
	appServerTestKeys(t, u, "@!a")
	id, target := u.picker.scanID, u.picker.target
	u.pickerKey("\x1b")
	u.applyPickerScan(pickerScanResult{id: id, target: target, cwd: u.session.cwd, choices: []composerChoice{{name: "a", path: "a"}}})
	if u.picker.open || len(u.picker.choices) != 0 {
		t.Fatal("canceled scan reopened picker")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	choices, problem := scanPickerFiles(ctx, u.session.cwd, "a")
	if len(choices) != 0 || problem != "" {
		t.Fatal("cancellation returned files or an error notice")
	}
}

func TestComposerFilePickerUsesWholeTokenAtCaret(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "see @src/main.go next"
	u.cursorBack = len("main.go next")
	target := u.completionTarget()
	if target.query != "src/main.go" || target.start != 4 || target.end != 16 {
		t.Fatalf("caret completion = %+v", target)
	}
	u.draft = "email@example.com"
	u.cursorBack = 0
	if u.completionTarget().kind != 0 {
		t.Fatal("email opened picker")
	}
}

func TestComposerFilePickerNarrowingClampsSelectedResult(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "@a")
	request := pickerRequests(t, w)[0]
	pickerReply(t, u, request.ID, `{"files":[{"root":"/work","path":"a.go"},{"root":"/work","path":"ab.go"}]}`)
	appServerTestKeys(t, u, "\x1b[Bb")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"files":[{"root":"/work","path":"ab.go"}]}`)
	appServerTestKeys(t, u, "\t")
	if u.draft != "@ab.go " {
		t.Fatalf("narrowed selection = %q", u.draft)
	}
}

func TestComposerFilePickerLateSkillSaveDoesNotReplaceResults(t *testing.T) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.cwd = t.TempDir()
	t.Cleanup(u.cancelPickerScan)
	if err := os.WriteFile(filepath.Join(u.session.cwd, "target.txt"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	u.picker.modal, u.picker.open = "manage", true
	u.picker.skills = []composerChoice{{name: "review", path: "/work/review/SKILL.md", enabled: true}}
	u.picker.skillsLoaded, u.picker.skillsCwd = true, u.session.cwd
	u.rememberSkillState()
	u.filterSkills("")
	u.toggleSkill()
	request := pickerRequests(t, w)[0]
	u.closeSkillsModal()
	appServerTestKeys(t, u, "@!target")
	select {
	case result := <-u.picker.scanResults:
		u.applyPickerScan(result)
	case <-time.After(5 * time.Second):
		t.Fatal("search timeout")
	}
	pickerReply(t, u, request.ID, `{"effectiveEnabled":false}`)
	if len(u.picker.choices) != 1 || u.picker.choices[0].path != "target.txt" {
		t.Fatalf("skill response corrupted file results: %+v", u.picker.choices)
	}
	appServerTestKeys(t, u, "\t")
	if u.draft != "@target.txt " {
		t.Fatalf("late save selection = %q", u.draft)
	}
}
