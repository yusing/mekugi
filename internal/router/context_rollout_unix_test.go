//go:build unix

package router

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestContextRolloutRejectsFIFOWithoutWriter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan activityPaneAgent, 1)
	go func() {
		var agent activityPaneAgent
		restoreContextUsage(&agent, appServerThreadInfo{ID: "thread", Path: path})
		done <- agent
	}()
	select {
	case agent := <-done:
		if agent.ContextKnown {
			t.Fatal("pipe became context evidence")
		}
	case <-time.After(time.Second):
		// Release and join the old blocking opener so the regression itself leaves
		// no goroutine or descriptor behind when it fails on the baseline.
		peer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("rollout reader did not exit after FIFO release")
		}
		t.Fatal("context restoration waited for a FIFO writer")
	}
}
