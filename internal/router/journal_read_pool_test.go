package router

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func journalReadFixture(t testing.TB) (string, threadJournal) {
	t.Helper()
	journal := threadJournal{
		Version: 2, Workspace: "/workspace", Thread: "root", Author: "/root", TreeAuthored: true,
		Receipts: map[string]journalReceipt{"published": {Digest: strings.Repeat("a", 64), IDs: []string{"/1"}}},
	}
	for i := range 64 {
		journal.Items = append(journal.Items, journalItem{
			ID: shortHandle(uint64(i)), Path: "/" + strconv.Itoa(i+1), Kind: "note", Title: "Measured result",
			Body: strings.Repeat("Retained evidence for the live performance diagnosis.\n", 32), Author: "/root",
		})
	}
	path := filepath.Join(t.TempDir(), journalFilename(journal.Workspace, journal.Thread))
	if err := os.WriteFile(path, mustMarshalJSON(journal), 0600); err != nil {
		t.Fatal(err)
	}
	return path, journal
}

func TestJournalRecordOwnsDecodedStorage(t *testing.T) {
	path, want := journalReadFixture(t)
	got, found, err := readJournalRecord(path)
	if err != nil || !found {
		t.Fatalf("read record: found=%v err=%v", found, err)
	}
	for range 8 {
		if _, found, err := readJournalRecord(path); err != nil || !found {
			t.Fatalf("repeat read: found=%v err=%v", found, err)
		}
	}
	if got.Workspace != want.Workspace || got.Thread != want.Thread ||
		got.Items[0].Body != want.Items[0].Body || got.Items[0].Path != "/1" ||
		got.Receipts["published"].Digest != want.Receipts["published"].Digest || got.Receipts["published"].IDs[0] != "/1" {
		t.Fatal("decoded journal changed after temporary buffers were released")
	}
}

func BenchmarkReadJournalRecord(b *testing.B) {
	path, _ := journalReadFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, found, err := readJournalRecord(path); err != nil || !found {
			b.Fatalf("read record: found=%v err=%v", found, err)
		}
	}
}
