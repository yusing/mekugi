package router

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativePaneStateResumeAndIsolation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := new(nativePanePersistence)
	if err := p.open(u.shell, "/workspace", "thread", false); err != nil { t.Fatal(err) }
	u.shell.focus, u.shell.split, u.shell.diffOpen = 1, 67, true
	u.shell.diff.navigation.columns = 22
	u.draft = "unsent private text"
	u.view.offset = 99
	if err := p.save(u.shell, time.Now(), true); err != nil { t.Fatal(err) }
	data, err := os.ReadFile(p.path)
	if err != nil { t.Fatal(err) }
	if bytes.Contains(data, []byte(u.draft)) || bytes.Contains(data, []byte("offset")) { t.Fatalf("saved non-layout state: %s", data) }
	info, err := os.Stat(p.path)
	if err != nil || info.Mode().Perm() != 0600 { t.Fatalf("state permissions: %v %v", info, err) }

	for _, tc := range []struct { workspace, thread string; resume, restored bool }{
		{"/workspace", "thread", true, true},
		{"/other", "thread", true, false},
		{"/workspace", "another-thread", true, false},
		{"/workspace", "thread", false, false},
	} {
		restored := newAppServerSessionTestUI(t, tc.workspace)
		q := new(nativePanePersistence)
		if err := q.open(restored.shell, tc.workspace, tc.thread, tc.resume); err != nil { t.Fatal(err) }
		if tc.restored {
			if restored.shell.paneState() != u.shell.paneState() { t.Fatalf("lost pane state: %+v", restored.shell.paneState()) }
			if restored.draft != "" || restored.view.offset != 0 { t.Fatal("restored out-of-scope view state") }
			var frame bytes.Buffer
			if err := restored.paint(&frame, 120, 35); err != nil { t.Fatal(err) }
			if restored.shell.layout.vertical != 67 || restored.shell.layout.diff.w == 0 { t.Fatalf("restored layout not rendered: %+v", restored.shell.layout) }
			if !strings.Contains(restored.shell.nativeStatus(), "s files") { t.Fatal("restored focus does not route Diff keys") }
			if err := restored.paint(&frame, 45, 14); err != nil { t.Fatal(err) }
			if restored.shell.layout.diff.w == 0 || restored.shell.layout.codex.w != 0 { t.Fatal("narrow screen did not retain focused pane") }
			if err := restored.paint(&frame, 100, 35); err != nil { t.Fatal(err) }
			if restored.shell.layout.vertical != 60 { t.Fatal("restored split not clamped to current terminal") }
		} else if restored.shell.focus != 0 || restored.shell.diffOpen || restored.shell.split != 0 {
			t.Fatalf("pane state leaked: %+v", tc)
		}
	}
}

func TestNativePaneStateDebounceAndExit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := new(nativePanePersistence)
	if err := p.open(u.shell, "/workspace", "thread", false); err != nil { t.Fatal(err) }
	now := time.Now()
	u.shell.split = 60
	if err := p.save(u.shell, now, false); err != nil { t.Fatal(err) }
	if _, err := os.Stat(p.path); !os.IsNotExist(err) { t.Fatalf("saved before debounce: %v", err) }
	u.shell.split = 63
	if err := p.save(u.shell, now.Add(200*time.Millisecond), false); err != nil { t.Fatal(err) }
	if err := p.save(u.shell, now.Add(300*time.Millisecond), false); err != nil { t.Fatal(err) }
	if _, err := os.Stat(p.path); !os.IsNotExist(err) { t.Fatalf("did not coalesce drag: %v", err) }
	if err := p.save(u.shell, now.Add(451*time.Millisecond), false); err != nil { t.Fatal(err) }
	before, err := os.Stat(p.path)
	if err != nil { t.Fatal(err) }
	if err := p.save(u.shell, now.Add(time.Second), false); err != nil { t.Fatal(err) }
	after, err := os.Stat(p.path)
	if err != nil || !os.SameFile(before, after) { t.Fatal("unchanged state rewritten") }
	u.shell.focus = 3
	if err := p.save(u.shell, now.Add(time.Second), true); err != nil { t.Fatal(err) }
	restored := newAppServerSessionTestUI(t, "/workspace")
	if err := new(nativePanePersistence).open(restored.shell, "/workspace", "thread", true); err != nil { t.Fatal(err) }
	if restored.shell.focus != 3 || restored.shell.split != 63 { t.Fatal("exit did not flush final state") }
}

func TestNativePaneStateInvalidAndUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	p := new(nativePanePersistence)
	if err := p.open(u.shell, "/workspace", "thread", false); err != nil { t.Fatal(err) }
	if err := os.MkdirAll(filepath.Dir(p.path), 0700); err != nil { t.Fatal(err) }
	for _, body := range []string{"{", `{"version":2}`, `{"version":1,"focus":99}`, `{"version":1,"focus":1,"diffOpen":false}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(p.path, []byte(body), 0600); err != nil { t.Fatal(err) }
		err := p.open(u.shell, "/workspace", "thread", true)
		if err == nil || u.shell.focus != 0 || u.shell.split != 0 { t.Fatalf("invalid state applied: %s %v", body, err) }
		u.paneError(err)
		if u.thread != "main" || !strings.Contains(u.view.entries[len(u.view.entries)-1].Text, "Pane layout") { t.Fatal("layout failure was fatal or invisible") }
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil { t.Fatal(err) }
	p.path = filepath.Join(blocked, "pane.json")
	u.shell.split = 60
	if err := p.save(u.shell, time.Now(), true); err == nil { t.Fatal("lost persistence failure") }
	if err := p.save(u.shell, time.Now().Add(time.Second), false); err != nil { t.Fatal("failed save retried every frame") }
}
