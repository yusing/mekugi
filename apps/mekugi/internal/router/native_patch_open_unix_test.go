//go:build unix

package router

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativePatchFileCaptureDoesNotWaitOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readNativePatchFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO was accepted as regular patch evidence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("patch observation blocked on FIFO")
	}
}
