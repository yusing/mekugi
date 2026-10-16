package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinToolWorkerExecutable(t *testing.T) {
	for _, state := range []string{"unchanged", "replaced", "removed"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			location := filepath.Join(directory, "installed")
			if err := os.WriteFile(location, []byte("original"), 0o700); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(location)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if state != "unchanged" {
				if err := os.Remove(location); err != nil {
					t.Fatal(err)
				}
			}
			if state == "replaced" {
				if err := os.WriteFile(location, []byte("replacement"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(directory, "pinned")
			if err := pinToolWorkerExecutable(source, location, target); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "original" {
				t.Fatalf("pinned %q, %v", content, err)
			}
			if err := pinToolWorkerExecutable(source, location, target); err == nil {
				t.Fatal("overwrote existing target")
			}
			content, err = os.ReadFile(target)
			if err != nil || string(content) != "original" {
				t.Fatalf("existing target changed: %q, %v", content, err)
			}
		})
	}
}

func TestPinToolWorkerExecutableDoesNotRetainLaunchSymlink(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(directory, "launch")
	if err := os.Symlink(installed, launch); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(launch)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target := filepath.Join(directory, "pinned")
	if err := pinToolWorkerExecutable(source, launch, target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("pin is not a regular file: %v", err)
	}
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, installed); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "original" {
		t.Fatalf("pinned launch follows replacement: %q, %v", content, err)
	}
}
