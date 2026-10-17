//go:build unix

package router

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativePaneStateRejectsFIFOWithoutWaiting(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ui := newAppServerSessionTestUI(t, "/workspace")
	preferences := testPanePreferences()
	path, err := nativePaneStatePath("/workspace", "thread")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- preferences.open(&ui.shell.Shell, &ui.mainView, "/workspace", "thread", true) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO admission: %v", err)
		}
	case <-time.After(time.Second):
		// Release and join an old blocking reader before failing the regression.
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatalf("release blocked preference read: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("preference reader did not stop after FIFO release")
		}
		t.Fatal("preference restoration waited for a FIFO writer")
	}
	if ui.shell.Focus != 0 || ui.shell.Split != 0 {
		t.Fatal("invalid preference changed layout")
	}
}
