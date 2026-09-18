package router

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/shellruntime"
)

func TestPreparedRequestStoresCurrentShellRuntime(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t)
	runtimePath, err := shellruntime.Path(proxy.shellDirectory, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if target != proxy.registry.shellRuntime {
		t.Fatalf("runtime target = %q, want %q", target, proxy.registry.shellRuntime)
	}
	scriptsPath, err := shellruntime.ScriptsPath(proxy.shellDirectory, "thread-1")
	if err != nil || transform.shellDirectory != scriptsPath {
		t.Fatalf("shell directory = %q, want %q: %v", transform.shellDirectory, scriptsPath, err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thread runtime survived proxy close: %v", err)
	}
}

func TestShellRuntimeRejectsTraversalAndSymlinkDirectories(t *testing.T) {
	proxy, _ := newShellStorageTestProxy(t)
	for _, id := range []string{"", "../outside", "../../../outside", `/absolute`, `a\b`, "bad\x00id"} {
		if _, err := proxy.storeShellRuntime(id); err == nil {
			t.Fatalf("accepted thread ID %q", id)
		}
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, ".runtime")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := shellruntime.ScriptsPath(proxy.shellDirectory, "symlink-scripts")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.storeShellRuntime("symlink-scripts"); err != nil {
		t.Fatalf("launcher depended on scripts: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
		t.Fatalf("outside launcher changed: %q, %v", got, err)
	}
}

func TestShellRuntimePreservesReplacementLocators(t *testing.T) {
	for _, replacement := range []string{"file", "directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			proxy, _ := newShellStorageTestProxy(t)
			path := testShellRuntimePath(t, proxy.shellDirectory, "thread-id")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("/another-router-worker", path); err != nil {
					t.Fatal(err)
				}
			}
			if err := proxy.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("replacement locator removed: %v", err)
			}
		})
	}
}
