//go:build unix

package waittargets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWaitTargetsRejectFIFO(t *testing.T) {
	if path := os.Getenv("MEKUGI_TEST_WAIT_FIFO"); path != "" {
		if _, found, err := Read(path); err == nil || found {
			t.Fatalf("FIFO accepted: found=%v err=%v", found, err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "wait.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWaitTargetsRejectFIFO$")
	cmd.Env = append(os.Environ(), "MEKUGI_TEST_WAIT_FIFO="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO reader blocked or failed: %v: %s", err, output)
	}
}
