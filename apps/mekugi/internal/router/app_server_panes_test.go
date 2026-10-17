package router

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/charmbracelet/x/ansi"
)

func TestNativePaneStateResumeAndIsolation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := testPanePreferences()
	if err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "thread", false); err != nil {
		t.Fatal(err)
	}
	terminalTest(u).Focus, terminalTest(u).Split, terminalTest(u).DiffOpen = 1, 67, true
	savedPane(terminalTest(u)).SetNavigatorColumns(22)
	u.Text = "unsent private text"
	u.shell.Transcript.Offset = 99
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), time.Now(), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testPanePath(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(u.Text)) || bytes.Contains(data, []byte("offset")) {
		t.Fatalf("saved non-layout state: %s", data)
	}
	info, err := os.Stat(testPanePath(t, p))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v %v", info, err)
	}

	for _, tc := range []struct {
		workspace, thread string
		resume, restored  bool
	}{
		{"/workspace", "thread", true, true},
		{"/other", "thread", true, false},
		{"/workspace", "another-thread", true, false},
		{"/workspace", "thread", false, false},
	} {
		restored := newAppServerSessionTestUI(t, tc.workspace)
		q := testPanePreferences()
		if err := q.open(&terminalTest(restored).Shell, restored.shell.Main, tc.workspace, tc.thread, tc.resume); err != nil {
			t.Fatal(err)
		}
		if tc.restored {
			if terminalTest(restored).Shell.Preferences(restored.shell.Main) != terminalTest(u).Shell.Preferences(u.shell.Main) {
				t.Fatalf("lost pane state: %+v", terminalTest(restored).Shell.Preferences(restored.shell.Main))
			}
			if restored.Text != "" || restored.shell.Transcript.Offset != 0 {
				t.Fatal("restored out-of-scope view state")
			}
			var frame bytes.Buffer
			if err := restored.testPaint(&frame, 120, 35); err != nil {
				t.Fatal(err)
			}
			if terminalTest(restored).Layout.Vertical != 67 || terminalTest(restored).Layout.Diff.W == 0 {
				t.Fatalf("restored layout not rendered: %+v", terminalTest(restored).Layout)
			}
			if !strings.Contains(ansi.Strip(terminalTest(restored).nativeStatus()), "s files") {
				t.Fatal("restored focus does not route Diff keys")
			}
			if err := restored.testPaint(&frame, 45, 14); err != nil {
				t.Fatal(err)
			}
			if terminalTest(restored).Layout.Diff.W == 0 || terminalTest(restored).Layout.Main.W != 0 {
				t.Fatal("narrow screen did not retain focused pane")
			}
			if err := restored.testPaint(&frame, 100, 35); err != nil {
				t.Fatal(err)
			}
			if terminalTest(restored).Layout.Vertical != 60 {
				t.Fatal("restored split not clamped to current terminal")
			}
		} else if terminalTest(restored).Focus != 0 || terminalTest(restored).DiffOpen || terminalTest(restored).Split != 0 {
			t.Fatalf("pane state leaked: %+v", tc)
		}
	}
}

func TestNativePaneStateDebounceAndExit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := testPanePreferences()
	if err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "thread", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	terminalTest(u).Split = 60
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(testPanePath(t, p)); !os.IsNotExist(err) {
		t.Fatalf("saved before debounce: %v", err)
	}
	terminalTest(u).Split = 63
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now.Add(200*time.Millisecond), false); err != nil {
		t.Fatal(err)
	}
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now.Add(300*time.Millisecond), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(testPanePath(t, p)); !os.IsNotExist(err) {
		t.Fatalf("did not coalesce drag: %v", err)
	}
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now.Add(451*time.Millisecond), false); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(testPanePath(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(testPanePath(t, p))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("unchanged state rewritten")
	}
	terminalTest(u).Focus = 3
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	restored := newAppServerSessionTestUI(t, "/workspace")
	if err := testPanePreferences().open(&terminalTest(restored).Shell, restored.shell.Main, "/workspace", "thread", true); err != nil {
		t.Fatal(err)
	}
	if terminalTest(restored).Focus != 3 || terminalTest(restored).Split != 63 {
		t.Fatal("exit did not flush final state")
	}
}

func TestNativePaneStateInvalidAndUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := testPanePreferences()
	if err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "thread", false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(testPanePath(t, p)), 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{", `{"version":2}`, `{"version":1,"focus":99,"settings":{"model":"invalid-layout","observed":"2026-10-01T00:00:00Z"}}`, `{"version":1,"focus":1,"diffOpen":false}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(testPanePath(t, p), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "thread", true)
		if err == nil || terminalTest(u).Focus != 0 || terminalTest(u).Split != 0 {
			t.Fatalf("invalid state applied: %s %v", body, err)
		}
		if state, readErr := p.read("/workspace", "thread"); readErr == nil || state.Settings.Model != "" {
			t.Fatal("invalid renderer state contributed saved settings")
		}
		u.dirty = paneError(u.attached.source, err) || u.dirty
		if u.Thread != "main" || !strings.Contains(u.attached.source.Entries[len(u.attached.source.Entries)-1].Source.Text, "Pane layout") {
			t.Fatal("layout failure was fatal or invisible")
		}
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocked)
	if err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "thread", false); err != nil {
		t.Fatal(err)
	}
	terminalTest(u).Split = 60
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), time.Now(), true); err == nil {
		t.Fatal("lost persistence failure")
	}
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), time.Now().Add(time.Second), false); err != nil {
		t.Fatal("failed save retried every frame")
	}
}

func testPreferences() *preferences.Service {
	service, err := preferences.New(testHistory())
	if err != nil {
		panic(err)
	}
	return service
}

func testPanePreferences() *nativePreferences { return &nativePreferences{service: testPreferences()} }

func nativePaneStatePath(workspace, thread string) (string, error) {
	base, err := history.StateDirectory()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(workspace + "\x00" + thread))
	return filepath.Join(base, "ui", fmt.Sprintf("panes-%x.json", key)), nil
}

func testPanePath(t *testing.T, p *nativePreferences) string {
	t.Helper()
	scope := p.attachment.Scope()
	path, err := nativePaneStatePath(scope.Workspace, scope.Thread)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
