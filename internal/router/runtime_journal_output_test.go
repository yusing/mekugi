package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRuntimeJournalLargeReadRetainsCompleteSnapshot(t *testing.T) {
	t.Parallel()
	mixed := strings.Repeat("x", 4000) + strings.Repeat("\x01", 2000) + strings.Repeat("\U0001f680", 2500)
	s, b, c := runtimeJournalFixture(t)
	ctx, err := s.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	title, kind := "Parent", "task"
	mutations := []journalMutation{{Op: "add", Kind: kind, Title: &title}}
	for i := range 82 {
		nodeTitle := fmt.Sprintf("Node %d", i)
		mutations = append(mutations, journalMutation{Op: "add", Under: "/1", Kind: "note", Title: &nodeTitle, Body: &mixed})
	}
	if _, err := s.journal.journals.apply(ctx, s.owner.store, b.Workspace, observationThread(b), "seed", mutations); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.journal.journals.readTree(ctx, s.owner.store, b.Workspace, observationThread(b), "", "", nil, "own")
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(map[string]any{"nodes": nodes})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) <= 2<<20 {
		t.Fatal("fixture must cross two retained chunk boundaries")
	}
	runtimeJournalReceipt(t, s, b, c, "read", "journal_read", `{"view":"own"}`)
	body := runtimeJournalInvoke(t, s, c, "read", "journal_read", `{"view":"own"}`)
	var result struct {
		Incomplete bool   `json:"incomplete"`
		Bytes      int    `json:"bytes"`
		NextCall   string `json:"next_call"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Incomplete || result.Bytes != len(want) || len(body) > 128<<10 || !strings.HasPrefix(result.NextCall, "mread ") {
		t.Fatalf("unbounded or invalid response: %s", body)
	}
	id := strings.TrimPrefix(result.NextCall, "mread ")
	// Use the public recovery consumer once. The rest of the check reads
	// saved chunks directly to avoid tokenizing megabytes in offline tests.
	page := executeMRead(ctx, toolWorkerManifest{ReplayDirectory: s.owner.store.directory}, []string{id, "--max-tokens", "100"})
	if page.ExitCode != 1 || !strings.Contains(page.Stdout, `"nodes"`) || !strings.Contains(page.Stderr, "next_call: mread ") {
		t.Fatalf("first continuation failed: %+v", page)
	}
	runtimeJournalAdd(t, s, b, c, "later", "New state must not change the snapshot")
	fresh := &mekugiReplayStore{directory: s.owner.store.directory}
	var recovered strings.Builder
	seen := make(map[string]bool)
	splitRune := false
	for id != "" {
		if seen[id] {
			t.Fatal("continuation loop")
		}
		seen[id] = true
		record, err := fresh.readShellOutput(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(record.Stdout) || len(record.Stdout) > (1<<20)+3 {
			t.Fatal("invalid chunk")
		}
		splitRune = splitRune || len(record.Stdout) > 1<<20
		recovered.WriteString(record.Stdout)
		if record.Stderr != "" {
			t.Fatal("chunk metadata became command stderr")
		}
		id = record.Next
	}
	if len(seen) < 3 {
		t.Fatal("snapshot must have two full chunks and a final partial chunk")
	}
	if !splitRune {
		t.Fatal("fixture must cross a multibyte chunk boundary")
	}
	if recovered.String() != string(want) {
		t.Fatal("large read lost, changed or reordered snapshot data")
	}
}

func TestRuntimeJournalLargeReadStorageFailureDoesNotExposeReference(t *testing.T) {
	s, b, _ := runtimeJournalFixture(t)
	ctx, err := s.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	s.owner.store.maxBytes = 1
	result, err := s.journal.readResult(ctx, []journalNode{{Path: "/1", Body: strings.Repeat("x", 129<<10)}})
	if err == nil || result != nil {
		t.Fatalf("storage failure claimed recovery: %v, %v", result, err)
	}
}

func TestRuntimeJournalChildReadUsesSharedNativeOutputNamespace(t *testing.T) {
	s, root, _ := runtimeJournalFixture(t)
	child := root
	child.Agent = "child"
	if err := s.owner.bind(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	childCtx, err := s.journal.scope(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	rootCtx, err := s.journal.scope(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	// Native children allocate distinct handles in their parent's session
	// namespace. A known reference is safe to read from the Bash frontend.
	rootID, err := s.owner.store.putShellOutput(rootCtx, "root-only", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	childID, err := s.owner.store.putShellOutput(childCtx, "child-only", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rootID == childID {
		t.Fatal("native output handle collision")
	}
	page := executeMRead(rootCtx, toolWorkerManifest{ReplayDirectory: s.owner.store.directory}, []string{childID})
	if page.ExitCode != 0 || page.Stdout != "child-only" {
		t.Fatalf("Bash recovery lost child output: %+v", page)
	}
}
