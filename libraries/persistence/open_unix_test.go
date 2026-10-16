//go:build unix

package persistence

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenRetainedFIFO(t *testing.T) {
	if path := os.Getenv("MEKUGI_TEST_RETAINED_FIFO"); path != "" {
		file, err := OpenRetained(path, 1024)
		if file != nil {
			file.Close()
		}
		if err == nil {
			t.Fatal("accepted FIFO")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("selection: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOpenRetainedFIFO$")
	cmd.Env = append(os.Environ(), "MEKUGI_TEST_RETAINED_FIFO="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO admission blocked or failed: %v: %s", err, output)
	}
}
