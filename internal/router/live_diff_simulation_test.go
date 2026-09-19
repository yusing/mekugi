package router

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveDiffSimulationRealGoFlows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"simulation": true}}}
	_, broker, _ := liveDiffTestBroker(t, store, scope)
	if err := playLiveDiffSimulation(ctx, broker, store, workspace, 20, false); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	handler := read("handler.go")
	if !strings.Contains(handler, "http.MethodPost") || strings.Contains(handler, "rejected requests") ||
		strings.Contains(handler, "InterruptedPreview") {
		t.Fatal("partial/rejected/interrupted flows left incorrect applied source")
	}
	routes := read("routes.go")
	for _, text := range []string{"/v2/001", "/v2/180", "Unicode 界", "WRAPPED_TIP", "ROUTES_READY"} {
		if !strings.Contains(routes, text) {
			t.Fatalf("creation/multi-hunk flow lost %q", text)
		}
	}
	for _, name := range []string{"audit.go", "lifecycle.go"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rename/delete left %s: %v", name, err)
		}
	}
	if read("notes.txt") != "Unicode 界 é without final newline" ||
		read("shell.log") != "checked fixture\nSHELL_TIP\n" ||
		read("standalone.log") != "standalone shell\nFUNCTIONS_SHELL_TIP\n" {
		t.Fatal("missing-newline or shell flow did not execute the fixture accurately")
	}
	files, err := store.liveDiffSnapshotFiles(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	foundRoutes := false
	for _, file := range files {
		foundRoutes = foundRoutes || file.Path == filepath.Join(workspace, "routes.go")
		if !strings.HasPrefix(file.Path, workspace+string(filepath.Separator)) {
			t.Fatalf("capture escaped fixture scope: %q", file.Path)
		}
	}
	if !foundRoutes {
		t.Fatal("real applied Go file is missing from captures")
	}
	broker.mu.Lock()
	active := len(broker.previews)
	broker.mu.Unlock()
	if active != 0 {
		t.Fatal("finished simulation retained active previews")
	}
	// The example is actual buildable Go, not syntax-shaped filler.
	command := exec.CommandContext(ctx, "go", "test", ".")
	command.Dir = workspace
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("simulated Go module is not usable: %v\n%s", err, output)
	}
}

func TestLiveDiffSimulationRepeatCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"simulation": true}},
	})
	sub := broker.subscribe()
	done := make(chan error, 1)
	go func() { done <- playLiveDiffSimulation(ctx, broker, store, workspace, 20, true) }()
	repeated := false
	for !repeated {
		select {
		case event := <-sub.events:
			repeated = strings.Contains(event.Status, "cycle 2")
		case err := <-done:
			t.Fatalf("repeat stopped before its second cycle: %v", err)
		case <-ctx.Done():
			t.Fatal("repeat did not reach its second cycle")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("repeat cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repeat worker outlived cancellation")
	}
	broker.mu.Lock()
	active := len(broker.previews)
	broker.mu.Unlock()
	if active != 0 {
		t.Fatal("cancelled repeat retained active preview")
	}
}
