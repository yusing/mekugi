package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecTrackCloseReleasesActiveReport(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	hub, err := listenExecTrack(ctx, filepath.Join(root, "requests"), filepath.Join(root, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.close)
	const script = "echo first; echo second"
	hub.start([3]string{"main", "turn", "item"}, "/bin/bash -c "+quoteShellWord(script))
	dialExecTrackReport(t, hub, script) // Keep the accepted producer connection open.
	closed := make(chan struct{})
	go func() { hub.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("tracking close did not settle its active report")
	}
	if ctx.Err() != nil {
		t.Fatal("tracking close cancelled its caller")
	}
	entries, err := os.ReadDir(hub.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("tracking close left active report resources: %v, %v", entries, err)
	}
}
