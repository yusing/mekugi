package persistence

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAtomicFilePublication(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "record")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicFile(path, ".pending-*", []byte("new"), nil); err != nil {
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
	if err := AtomicFile(blocked, ".pending-*", []byte("new"), nil); err == nil {
		t.Fatal("rename over directory succeeded")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary file leaked: %v %v", entries, err)
	}
}

// A short write can include a successful prefix even though the operation fails.
type partialWriter struct{}

func (partialWriter) Write(p []byte) (int, error) { return len(p) / 2, io.ErrShortWrite }

func TestCounterActualWrites(t *testing.T) {
	counter := new(Counter)
	if n, err := counter.Writer(partialWriter{}).Write([]byte("abcdef")); n != 3 || err != io.ErrShortWrite {
		t.Fatalf("write = %d, %v", n, err)
	}
	first := counter.Snapshot()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { _, _ = counter.Writer(io.Discard).Write([]byte("xx")) })
	}
	wg.Wait()
	if first.Bytes != 3 || counter.Snapshot().Bytes != 23 {
		t.Fatalf("snapshots: %v, %v", first, counter.Snapshot())
	}
	if (*Counter)(nil).Snapshot() != nil {
		t.Fatal("disabled measurement reported zero")
	}
}

func TestAtomicFileAccountsFailedPublication(t *testing.T) {
	counter := new(Counter)
	directory := t.TempDir()
	if err := AtomicFile(directory, "pending-*", []byte("written"), counter); err == nil {
		t.Fatal("replaced directory")
	}
	if counter.Snapshot().Bytes != 7 {
		t.Fatalf("failed rename lost writes: %v", counter.Snapshot())
	}
	if err := AtomicFile(filepath.Join(directory, "record"), "pending-*", []byte("ok"), counter); err != nil {
		t.Fatal(err)
	}
	if counter.Snapshot().Bytes != 9 {
		t.Fatalf("wrong total: %v", counter.Snapshot())
	}
}
