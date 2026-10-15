package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

func TestUISnapshotNativeThreadSwitch(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.view.clock, u.agents.clock = u.clock, u.clock
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reader"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": thread + " result\n\n" + strings.Join([]string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta"}, "\n\n")}})
	}
	screen := vt.NewEmulator(140, 36)
	defer screen.Close()
	paint := func(name string) string {
		t.Helper()
		if err := u.paint(screen, 140, 36); err != nil {
			t.Fatal(err)
		}
		frame := screen.String()
		assertNativeUISnapshot(t, name, strings.Split(frame, "\n"))
		return frame
	}
	keys := func(input string) {
		t.Helper()
		for _, key := range []byte(input) {
			if err := u.shell.key(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	paint("native-thread-main-full")
	if u.shell.layout.codex.w != 138 || u.shell.layout.vertical != -1 {
		t.Fatalf("initial geometry: %+v", u.shell.layout)
	}
	click := func(agent string) {
		t.Helper()
		for _, hit := range u.agents.hits {
			if hit.agent == agent && !hit.skills {
				r := u.shell.layout.roster
				for _, suffix := range []string{"M", "m"} {
					if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", r.x+hit.first, r.y+hit.row, suffix)); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
		}
		t.Fatalf("missing roster target %q", agent)
	}
	click("/root/reader")
	frame := paint("native-thread-child-full")
	if !strings.Contains(frame, "child result") || !strings.Contains(frame, "eta") || strings.Contains(frame, "main result") || !strings.Contains(frame, "Input: Main") || u.thread != "main" {
		t.Fatalf("child view or input identity: %s", frame)
	}
	keys("\x022")
	paint("native-thread-child-split")
	if u.shell.layout.diff.w == 0 || !u.agents.only {
		t.Fatal("opening Diff lost the displayed child")
	}
	keys("\x022")
	paint("native-thread-child-unsplit")
	if u.shell.layout.codex.w != 138 || u.shell.layout.diff.w != 0 || u.shell.focus != 0 {
		t.Fatal("closing Diff did not reclaim its width")
	}
	keys("/skills\r")
	frame = paint("native-thread-child-picker")
	if !u.picker.open || !strings.Contains(frame, "child result") || strings.Contains(frame, "main result") {
		t.Fatal("Main's picker changed the displayed transcript")
	}
	keys("\x1b")
	u.shell.flushEscape()
	// Metadata discovery is independent of the composer destination.
	appServerOneRequest(t, u.client.Input.(*appServerTestInput), "thread/read", "")
	// Typing still edits Main, and returning to it preserves that draft.
	keys("continue")
	if u.draft != "continue" || u.thread != "main" {
		t.Fatal("transcript navigation changed the input destination")
	}
	keys("\r")
	request := appServerOneRequest(t, u.client.Input.(*appServerTestInput), "turn/start", "continue")
	if request.Params.ThreadID != "main" {
		t.Fatal("submission targeted the displayed child")
	}
	u.draft = "retained draft"
	click("/root")
	frame = paint("native-thread-return-main")
	if u.agents.only || !strings.Contains(frame, "main result") || strings.Contains(frame, "Input: Main") || u.draft != "retained draft" {
		t.Fatal("returning to Main lost its transcript or draft")
	}
}

func TestUISnapshotNativeTinyDiffToggle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.ensureShell()
	screen := vt.NewEmulator(80, 6)
	defer screen.Close()
	for _, key := range []byte("\x022") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.paint(screen, 80, 6); err != nil {
		t.Fatal(err)
	}
	assertNativeUISnapshot(t, "native-thread-tiny-diff", strings.Split(screen.String(), "\n"))
	if u.shell.layout.diff.h == 0 || !strings.Contains(screen.String(), "CONNECTING") {
		t.Fatal("opening Diff left a short terminal blank")
	}
	for _, key := range []byte("\x022") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.paint(screen, 80, 6); err != nil {
		t.Fatal(err)
	}
	if u.shell.layout.codex.w != 80 || u.shell.layout.diff.h != 0 {
		t.Fatal("closing Diff did not restore the short transcript")
	}
}
