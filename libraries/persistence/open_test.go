package persistence

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenRetainedReplacement(t *testing.T) {
	for _, replacement := range []string{"regular", "symlink", "directory", "oversized"} {
		t.Run(replacement, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "record")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("selection: %v", err)
			}
			candidate := filepath.Join(dir, "replacement")
			switch replacement {
			case "directory":
				if err := os.Mkdir(candidate, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, candidate); err != nil {
					if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
						t.Skipf("symlink fixture unavailable: %v", err)
					}
					t.Fatal(err)
				}
			default:
				value := "new"
				if replacement == "oversized" {
					value = "too large"
				}
				if err := os.WriteFile(candidate, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if replacement == "directory" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(candidate, path); err != nil {
				t.Fatal(err)
			}
			file, err := OpenRetained(path, 3)
			if replacement != "regular" {
				if file != nil {
					file.Close()
				}
				if err == nil {
					t.Fatal("accepted replacement")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || string(body) != "new" {
				t.Fatalf("published replacement: %q, %v", body, err)
			}
		})
	}
}
