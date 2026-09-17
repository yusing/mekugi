package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicFilePublication(t *testing.T) {
	for _, syncFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "cache", true: "durable"}[syncFile], func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "record")
			if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := writeAtomicFile(path, ".pending-*", []byte("new"), syncFile); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "new" {
				t.Fatalf("content=%q err=%v", content, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private file mode not preserved: %v %v", info, err)
			}
			blocked := filepath.Join(directory, "directory")
			if err := os.Mkdir(blocked, 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeAtomicFile(blocked, ".pending-*", []byte("new"), syncFile); err == nil {
				t.Fatal("rename over directory succeeded")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 2 {
				t.Fatalf("temporary file leaked: %v %v", entries, err)
			}
		})
	}
}
