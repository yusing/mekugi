package messagehistory

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHistoryStockFormatAndRetention(t *testing.T) {
	store := Store{Directory: t.TempDir()}
	path := filepath.Join(store.Directory, "history.jsonl")
	if err := os.WriteFile(path, []byte("{\"session_id\":\"stock\",\"ts\":1,\"text\":\"stock input\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(t.Context(), "mekugi", "foo\nbar"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(t.Context())
	if err != nil || (len(got) != 2 || got[0].Text != "stock input" || got[1].Text != "foo\nbar") {
		t.Fatalf("history=%+v err=%v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var record Entry
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil || record.SessionID != "mekugi" || record.Timestamp == 0 || record.Text != "foo\nbar" {
		t.Fatalf("stock record=%+v err=%v", record, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v", info.Mode())
	}
	store.Config.MaxBytes = 90
	for _, text := range []string{"old", "new", strings.Repeat("large", 30)} {
		if err := store.Append(t.Context(), "mekugi", text); err != nil {
			t.Fatal(err)
		}
	}
	got, err = store.Read(t.Context())
	if err != nil || len(got) != 1 || got[0].Text != strings.Repeat("large", 30) {
		t.Fatalf("retention=%+v err=%v", got, err)
	}
	before, _ := os.ReadFile(path)
	store.Config.Persistence = "none"
	if err := store.Append(t.Context(), "mekugi", "not saved"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("history.persistence=none wrote history")
	}
}

func TestHistoryCorruptionAndReadLimit(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "history.jsonl")
	var data strings.Builder
	data.WriteString(strings.Repeat("x", readLimit))
	data.WriteByte('\n')
	for range 110 {
		data.WriteString("{\"session_id\":\"stock\",\"ts\":1,\"text\":\"valid\"}\n")
	}
	data.WriteString("broken\n{\"text\":\"incomplete")
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := (Store{Directory: directory}).Read(t.Context())
	if err == nil || len(got) != 99 {
		t.Fatalf("partial history=%d err=%v", len(got), err)
	}
}

func TestHistoryLockContention(t *testing.T) {
	store := Store{Directory: t.TempDir()}
	if err := store.Append(t.Context(), "one", "first"); err != nil {
		t.Fatal(err)
	}
	held, err := store.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := store.Append(ctx, "two", "blocked"); err == nil {
		t.Fatal("append bypassed stock file lock")
	}
	held.Close()
	got, err := store.Read(t.Context())
	if err != nil || (len(got) != 1 || got[0].Text != "first") {
		t.Fatalf("locked write changed history=%+v err=%v", got, err)
	}
}

func TestHistoryDirectoryUsesSelectedProcess(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		got, err := Directory([]string{"Codex_Home=" + root}, root)
		if err != nil || got != root {
			t.Fatalf("mixed-case override=%q err=%v", got, err)
		}
	}
	for _, tc := range []struct {
		environment []string
		want        string
	}{
		{[]string{"CODEX_HOME=selected"}, filepath.Join(root, "selected")},
		{[]string{"HOME=" + root, "USERPROFILE=" + root}, filepath.Join(root, ".codex")},
		{[]string{"CODEX_HOME=wrong", "CODEX_HOME=" + root}, root},
	} {
		got, err := Directory(tc.environment, root)
		if err != nil || got != tc.want {
			t.Fatalf("directory=%q want=%q err=%v", got, tc.want, err)
		}
	}
	if _, err := Directory([]string{"CODEX_HOME=relative"}, ""); err == nil {
		t.Fatal("relative path borrowed router cwd")
	}
}

func TestHistoryUserManagedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "shared.jsonl")
	if err := os.WriteFile(target, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "history.jsonl")); err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: directory}
	if err := store.Append(t.Context(), "session", "linked"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(t.Context())
	if err != nil || len(got) != 1 || got[0].Text != "linked" {
		t.Fatalf("symlink recall=%+v err=%v", got, err)
	}
}

func TestHistoryRetentionCopiesCompleteLargeRecords(t *testing.T) {
	store := Store{Directory: t.TempDir()}
	for _, text := range []string{strings.Repeat("old", 30000), strings.Repeat("kept", 18000)} {
		if err := store.Append(t.Context(), "session", text); err != nil {
			t.Fatal(err)
		}
	}
	store.Config.MaxBytes = 120000
	if err := store.Append(t.Context(), "session", "newest"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Read(t.Context())
	if err != nil || len(entries) != 2 || entries[0].Text != strings.Repeat("kept", 18000) || entries[1].Text != "newest" {
		t.Fatalf("retention count=%d err=%v", len(entries), err)
	}
}
