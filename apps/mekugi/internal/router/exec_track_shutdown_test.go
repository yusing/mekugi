package router

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestExecTrackCloseReleasesActiveReport(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hub := newExecTrackFixture(t, ctx, execInstrumentation{})
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
	if _, err := os.Stat(hub.server.Endpoints().Directory); !os.IsNotExist(err) {
		t.Fatalf("tracking close left active report resources: %v", err)
	}
}
