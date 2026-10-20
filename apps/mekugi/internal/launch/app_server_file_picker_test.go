package launch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/conversation/completion"
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
	if len(choices) != 1 || choices[0].Path != "node_modules/excluded-module.js" {
		t.Fatalf("@! should include ignored files, not VCS metadata: %+v", choices)
	}
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.attached.cwd = root
	t.Cleanup(u.picker.cancelScan)
	appServerTestKeys(t, u, "@!excluded")
	if len(pickerRequests(t, w)) != 0 {
		t.Fatal("@! sent an ignore-respecting host search")
	}
	select {
	case result := <-u.picker.scanResults:
		u.dirty = u.picker.applyScan(u.attached.cwd, u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()), result) || u.dirty
	case <-time.After(5 * time.Second):
		t.Fatal("excluded search did not finish")
	}
	if len(u.picker.Choices) != 1 || u.picker.Loading {
		t.Fatalf("excluded picker = %+v", u.picker)
	}
	appServerTestKeys(t, u, "\t")
	if strings.Contains(u.Text, "@!") || !strings.Contains(u.Text, "excluded") {
		t.Fatalf("modifier leaked into selection: %q", u.Text)
	}
}

func TestComposerFilePickerNormalHidesGitMetadata(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "@g")
	request := pickerRequests(t, w)[0]
	pickerReply(t, u, request.ID, `{"files":[{"root":"/work","path":".git","match_type":"directory"},{"root":"/work","path":".git/config"},{"root":"/work","path":"nested/.git/HEAD"},{"root":"/work","path":"git.go"}]}`)
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Path != "git.go" {
		t.Fatalf("normal @ exposes git metadata: %+v", u.picker.Choices)
	}
}

func TestComposerFilePickerCanceledResultsCannotReopen(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ctx = t.Context()
	u.attached.cwd = t.TempDir()
	t.Cleanup(u.picker.cancelScan)
	appServerTestKeys(t, u, "@!a")
	id, target := u.picker.scanID, u.picker.Target
	u.pickerKey("\x1b")
	u.dirty = u.picker.applyScan(u.attached.cwd, u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()), pickerScanResult{id: id, target: target, cwd: u.attached.cwd, choices: []completion.Choice{{Name: "a", Path: "a"}}}) || u.dirty
	if u.picker.Open || len(u.picker.Choices) != 0 {
		t.Fatal("canceled scan reopened picker")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	choices, problem := scanPickerFiles(ctx, u.attached.cwd, "a")
	if len(choices) != 0 || problem != "" {
		t.Fatal("cancellation returned files or an error notice")
	}
}

func TestComposerFilePickerUsesWholeTokenAtCaret(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "see @src/main.go next"
	u.CursorBack = len("main.go next")
	target := u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode())
	if target.Query != "src/main.go" || target.Start != 4 || target.End != 16 {
		t.Fatalf("caret completion = %+v", target)
	}
	u.Text = "email@example.com"
	u.CursorBack = 0
	if u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()).Kind != 0 {
		t.Fatal("email opened picker")
	}
}

func TestComposerFilePickerNarrowingClampsSelectedResult(t *testing.T) {
	u, w := newAppServerTestUI()
	u.attached.cwd = "/work"
	appServerTestKeys(t, u, "@a")
	request := pickerRequests(t, w)[0]
	pickerReply(t, u, request.ID, `{"files":[{"root":"/work","path":"a.go"},{"root":"/work","path":"ab.go"}]}`)
	appServerTestKeys(t, u, "\x1b[Bb")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"files":[{"root":"/work","path":"ab.go"}]}`)
	appServerTestKeys(t, u, "\t")
	if u.Text != "@ab.go " {
		t.Fatalf("narrowed selection = %q", u.Text)
	}
}

func TestComposerFilePickerLateSkillSaveDoesNotReplaceResults(t *testing.T) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.attached.cwd = t.TempDir()
	t.Cleanup(u.picker.cancelScan)
	if err := os.WriteFile(filepath.Join(u.attached.cwd, "target.txt"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	u.picker.Kind, u.picker.Open = "manage", true
	u.picker.Skills = []completion.Choice{{Name: "review", Path: "/work/review/SKILL.md", Enabled: true}}
	u.picker.SkillsLoaded, u.picker.SkillsCwd = true, u.attached.cwd
	u.picker.RememberSkills()
	u.picker.FilterSkills("", u.picker.Kind == "manage")
	toggleSkill(&u.picker, u.Requests, u.attached.cwd, u.picker.Choices[0])
	request := pickerRequests(t, w)[0]
	u.picker.closeSkillsModal(&u.attached, u.shell, u.clock, u.Editor)
	appServerTestKeys(t, u, "@!target")
	select {
	case result := <-u.picker.scanResults:
		u.dirty = u.picker.applyScan(u.attached.cwd, u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()), result) || u.dirty
	case <-time.After(5 * time.Second):
		t.Fatal("search timeout")
	}
	pickerReply(t, u, request.ID, `{"effectiveEnabled":false}`)
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Path != "target.txt" {
		t.Fatalf("skill response corrupted file results: %+v", u.picker.Choices)
	}
	appServerTestKeys(t, u, "\t")
	if u.Text != "@target.txt " {
		t.Fatalf("late save selection = %q", u.Text)
	}
}
