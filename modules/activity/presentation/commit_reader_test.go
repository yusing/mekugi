package presentation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func commitGit(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func awaitCommit(t *testing.T, reader *CommitReader, key CommitKey) []ChangeRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for {
		if rows, ready := reader.Request(key); ready {
			return rows
		}
		select {
		case <-reader.Updates():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestCommitReaderScopeCacheAndImmutableRows(t *testing.T) {
	commitGit(t, `printf '%s\n' "$@" > "$2/args"
printf '2\t1\t%s.go\n' "${13}"
`)
	reader := NewCommitReader(t.Context())
	defer reader.Close()
	first := CommitKey{t.TempDir(), "abc1234"}
	rows := awaitCommit(t, reader, first)
	if len(rows) != 1 || rows[0].Added != 2 || rows[0].Removed != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	args, err := os.ReadFile(filepath.Join(first.Directory, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-C", first.Directory, "-c", "core.quotepath=off", "show", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--numstat", "--summary", "--format=", first.Hash + "^{commit}", "--"}
	if !reflect.DeepEqual(strings.Split(strings.TrimSpace(string(args)), "\n"), want) {
		t.Fatalf("arguments: %s", args)
	}
	rows[0].Added = 999
	if err := os.Remove(filepath.Join(first.Directory, "args")); err != nil {
		t.Fatal(err)
	}
	if cached := awaitCommit(t, reader, first); cached[0].Added != 2 {
		t.Fatal("caller mutated cached rows")
	}
	if _, err := os.Stat(filepath.Join(first.Directory, "args")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cached commit reread: %v", err)
	}
	second := CommitKey{t.TempDir(), first.Hash}
	awaitCommit(t, reader, second)
	if _, err := os.Stat(filepath.Join(second.Directory, "args")); err != nil {
		t.Fatal("same hash borrowed another directory's read", err)
	}
	second.Hash = "def5678"
	awaitCommit(t, reader, second)
	args, err = os.ReadFile(filepath.Join(second.Directory, "args"))
	if err != nil || !strings.Contains(string(args), "def5678^{commit}") {
		t.Fatalf("different hash reused read: %s, %v", args, err)
	}
	replacement := NewCommitReader(t.Context())
	defer replacement.Close()
	awaitCommit(t, replacement, first)
	if _, err := os.Stat(filepath.Join(first.Directory, "args")); err != nil {
		t.Fatal("attachments shared cache", err)
	}
}

func TestCommitReaderCancellationAndCapacity(t *testing.T) {
	commitGit(t, `: > "$2/started"
exec sleep 30
`)
	reader := NewCommitReader(t.Context())
	defer reader.Close()
	keys := []CommitKey{{t.TempDir(), "abc1234"}, {t.TempDir(), "def5678"}, {t.TempDir(), "cde1234"}}
	for _, key := range keys {
		if _, ready := reader.Request(key); ready {
			t.Fatal("pending read completed synchronously")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, first := os.Stat(filepath.Join(keys[0].Directory, "started"))
		_, second := os.Stat(filepath.Join(keys[1].Directory, "started"))
		if first == nil && second == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readers did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(keys[2].Directory, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader bound exceeded")
	}
	reader.Close()
	deadline = time.Now().Add(time.Second)
	for {
		reader.mu.Lock()
		active, cached := len(reader.active), len(reader.reads)
		reader.mu.Unlock()
		if cached != 0 {
			t.Fatal("canceled read cached an outcome")
		}
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled readers did not retire")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ready := reader.Request(keys[2]); ready {
		t.Fatal("retired reader accepted work")
	}
}

func TestCommitReaderFailureAndOutputBound(t *testing.T) {
	commitGit(t, "exit 1\n")
	reader := NewCommitReader(t.Context())
	defer reader.Close()
	key := CommitKey{t.TempDir(), "abc1234"}
	if rows := awaitCommit(t, reader, key); rows != nil {
		t.Fatalf("failed read: %+v", rows)
	}
	fallback := []ChangeRow{{Code: "A", Label: "new.go"}, {Footer: true, Label: key.Hash}}
	if got := CommitChanges(fallback, key, nil); !reflect.DeepEqual(got, fallback) {
		t.Fatal("failure lost output evidence")
	}
	var bounded vcsBoundedBuffer
	if _, err := bounded.Write(make([]byte, gitShowBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := bounded.Write([]byte{1}); !errors.Is(err, errGitShowBound) || !bounded.overflow {
		t.Fatal("output exceeded bound", err)
	}
}

func TestCommitReaderTimeout(t *testing.T) {
	commitGit(t, "exec sleep 30\n")
	reader := NewCommitReader(t.Context())
	defer reader.Close()
	if rows := awaitCommit(t, reader, CommitKey{t.TempDir(), "abc1234"}); rows != nil {
		t.Fatalf("timed-out read supplied rows: %+v", rows)
	}
}
