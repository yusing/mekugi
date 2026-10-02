package router

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeTestClient struct {
	events     chan session.Event
	sent       []string
	decisions  []session.Decision
	interrupts int
}

func (f *runtimeTestClient) Events() <-chan session.Event { return f.events }
func (f *runtimeTestClient) Send(_ context.Context, s string) error {
	f.sent = append(f.sent, s)
	return nil
}
func (f *runtimeTestClient) Respond(_ context.Context, d session.Decision) error {
	f.decisions = append(f.decisions, d)
	return nil
}
func (f *runtimeTestClient) Interrupt(context.Context) error { f.interrupts++; return nil }
func (*runtimeTestClient) Close() error                      { return nil }

func runtimeTestUI(t *testing.T) (*appServerUI, *runtimeTestClient) {
	t.Helper()
	f := &runtimeTestClient{events: make(chan session.Event, 16)}
	u := newRuntimeUI(t.Context(), f, "Claude Code", "/work")
	u.runtimeEvent(session.Event{Kind: "ready"})
	u.clock = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local) }
	u.thread, u.model = "native-session", "claude-sonnet"
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	return u, f
}
func runtimeKeys(t *testing.T, u *appServerUI, s string) {
	t.Helper()
	for _, b := range []byte(s) {
		if err := u.shell.key(b); err != nil {
			t.Fatal(err)
		}
	}
}
func runtimeFrame(t *testing.T, u *appServerUI, w, h int) string {
	t.Helper()
	s := vt.NewEmulator(w, h)
	defer s.Close()
	u.shell.paintedRows = nil
	if err := u.paint(s, w, h); err != nil {
		t.Fatal(err)
	}
	return s.String()
}
func TestNativeRuntimeSameComposerAndIntent(t *testing.T) {
	u, f := runtimeTestUI(t)
	runtimeKeys(t, u, "hello 世界\x1b[D\x7f")
	if u.draft != "hello 界" {
		t.Fatalf("shared grapheme edit: %q", u.draft)
	}
	runtimeKeys(t, u, "\x1a")
	if u.draft != "hello 世界" {
		t.Fatalf("undo: %q", u.draft)
	}
	runtimeKeys(t, u, "\r")
	if len(f.sent) != 1 || f.sent[0] != "hello 世界" {
		t.Fatalf("native input: %q", f.sent)
	}
	runtimeKeys(t, u, "kept\r")
	if u.draft != "kept" || len(f.sent) != 1 {
		t.Fatal("busy draft lost or sent")
	}
	runtimeKeys(t, u, "\x03\x03")
	if f.interrupts != 1 {
		t.Fatal("native interrupt not dispatched")
	}
	if err := u.runtimeEvent(session.Event{Kind: "done"}); err != nil {
		t.Fatal(err)
	}
	runtimeKeys(t, u, "\x1b[A")
	if u.draft != "hello 世界" {
		t.Fatalf("history: %q", u.draft)
	}
	if u.client != nil || u.proxy != nil {
		t.Fatal("Codex transport/router constructed")
	}
}
func TestNativeRuntimePermissionsUseExistingQuestionDock(t *testing.T) {
	u, f := runtimeTestUI(t)
	runtimeKeys(t, u, "parked draft")
	p := &session.Prompt{ID: "allow", Tool: "Bash", Description: "echo **literal** `value`"}
	if err := u.runtimeEvent(session.Event{Kind: "prompt", Prompt: p}); err != nil {
		t.Fatal(err)
	}
	runtimeFrame(t, u, 100, 28)
	runtimeKeys(t, u, "1\r")
	if len(f.decisions) != 1 || !f.decisions[0].Allow || f.decisions[0].ID != "allow" {
		t.Fatalf("decision: %+v", f.decisions)
	}
	if u.draft != "parked draft" {
		t.Fatal("parked draft lost")
	}
	p.ID = "deny"
	u.runtimeEvent(session.Event{Kind: "prompt", Prompt: p})
	runtimeFrame(t, u, 100, 28)
	runtimeKeys(t, u, "\x03")
	if len(f.decisions) != 2 || f.decisions[1].Allow {
		t.Fatal("decline not returned")
	}
}
func TestNativeRuntimeNoCodexActions(t *testing.T) {
	for _, text := range []string{"/model", "/status", "/btw hi", "/resume", "@file", "$skill"} {
		t.Run(text, func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			runtimeKeys(t, u, text)
			runtimeFrame(t, u, 100, 28)
			if strings.HasPrefix(text, "/") {
				runtimeKeys(t, u, "\r")
			}
		})
	}
}
func TestUISnapshotNativeRuntimeSameShell(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			u.runtimeEntry(session.Event{Kind: "message", ID: "u", Role: "You", Text: "Use the existing Mekugi UI."})
			u.runtimeEntry(session.Event{Kind: "message", ID: "m", Role: "Claude", Text: "The **same shell**, composer, and panes are in use."})
			u.runtimeEntry(session.Event{Kind: "tool", ID: "tool", Role: "Bash", Text: "{\"command\":\"echo **literal**\"}"})
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-main-%d.txt", width)), runtimeFrame(t, u, width, 28))
			u.runtimeEvent(session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "p", Tool: "Bash", Description: "{\"command\":\"echo **literal** `code` [link](path)\"}"}})
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-permission-%d.txt", width)), runtimeFrame(t, u, width, 28))
		})
	}
}

func TestNativeRuntimePastedAnswersPreserveUserText(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(fmt.Sprint(multiple), func(t *testing.T) {
			u, f := runtimeTestUI(t)
			u.runtimeEvent(session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "q", Tool: "AskUserQuestion", Questions: []session.Question{{Text: "Choose", Multiple: multiple, Options: []session.Option{{Label: "A"}, {Label: "B"}}}}}})
			runtimeFrame(t, u, 100, 28)
			runtimeKeys(t, u, "\x1b[200~custom\nanswer\x1b[201~\r")
			if len(f.decisions) != 1 || f.decisions[0].Answers["Choose"] != "custom\nanswer" {
				t.Fatalf("pasted answer changed: %+v", f.decisions)
			}
		})
	}
	u, f := runtimeTestUI(t)
	runtimeKeys(t, u, "\x1b[200~/tmp/picture.png\x1b[201~")
	if u.draft != "/tmp/picture.png" || len(f.sent) != 0 || len(u.images) != 0 {
		t.Fatal("native paste became an attachment or submitted input")
	}
}

func TestUISnapshotNativeRuntimeCancelledPrompt(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, f := runtimeTestUI(t)
			u.runtimeEvent(session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "p", Tool: "Bash", Description: "Native permission"}})
			u.runtimeEvent(session.Event{Kind: "dismiss", ID: "p"})
			u.runtimeEvent(session.Event{Kind: "dismiss", ID: "p"})
			if len(f.decisions) != 0 || u.questions.active != nil || !u.questions.calls[0].resolved || u.questions.calls[0].questions[0].outcome != "cancelled" {
				t.Fatal("cancellation did not retire the native prompt without sending an answer")
			}
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-cancelled-%d.txt", width)), runtimeFrame(t, u, width, 28))
		})
	}
}

func TestNativeRuntimeCommandsAndStartupAdmission(t *testing.T) {
	u, f := runtimeTestUI(t)
	u.runtime.ready = false
	runtimeKeys(t, u, "kept\r")
	if len(f.sent) != 0 || u.draft != "kept" {
		t.Fatal("input escaped startup gate")
	}
	u.runtimeEvent(session.Event{Kind: "ready", CommandInfo: []session.Command{{Name: "compact"}}})
	runtimeKeys(t, u, "\r")
	if len(f.sent) != 1 || f.sent[0] != "kept" {
		t.Fatal("ready did not admit unchanged draft")
	}
	u.runtimeEvent(session.Event{Kind: "done"})
	runtimeKeys(t, u, "/btw unadvertised\r")
	if len(f.sent) != 1 || u.draft != "/btw unadvertised" {
		t.Fatal("unadvertised command executed or draft lost")
	}
	runtimeKeys(t, u, "\x03/compact keep intent\r")
	if len(f.sent) != 2 || f.sent[1] != "/compact keep intent" {
		t.Fatalf("native command changed: %+v", f.sent)
	}
}
