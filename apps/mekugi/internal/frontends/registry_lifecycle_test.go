package frontends

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendRegistryRejectsRelativeRuntimeEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", "relative-runtime")
	registry, err := New(t.Context(), Config{Commands: testCommands()})
	if registry != nil {
		defer registry.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("registry=%v error=%v", registry, err)
	}
	if _, err := os.Stat("relative-runtime"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("relative runtime was created: %v", err)
	}
}

func TestFrontendRegistryCleanupPreservesPeerInvocation(t *testing.T) {
	directory := t.TempDir()
	replay := filepath.Join(directory, "replay")
	if _, err := testHistory().Open(t.Context(), replay); err != nil {
		t.Fatal(err)
	}
	config := Config{Commands: testCommands(), RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: replay}
	first, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})
	peer, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Error(err)
		}
	})
	if first.SnapshotDir == peer.SnapshotDir || first.FrontendDirectory == peer.FrontendDirectory {
		t.Fatal("independent registries share invocation resources")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.SnapshotDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed snapshot remains: %v", err)
	}
	path := filepath.Join(directory, "source.txt")
	if err := os.WriteFile(path, []byte("peer survives\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", uniqueTestThread("peer"))
	var stdout, stderr bytes.Buffer
	handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), peer.Frontends["mcat"], []string{path}, nil, &stdout, &stderr)
	if !handled || status != 0 || stdout.String() != "peer survives\n" || stderr.Len() != 0 {
		t.Fatalf("peer after cleanup: handled=%t status=%d stdout=%q stderr=%q", handled, status, &stdout, &stderr)
	}
}
