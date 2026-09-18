package router

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestShortHandlesRoundTripAndTokenCost(t *testing.T) {
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	maxTokens := 0
	seen := make(map[string]bool)
	for n := range uint64(4096) {
		id := shortHandle(n)
		got, ok := parseShortHandle(id)
		if !ok || got != n || seen[id] {
			t.Fatalf("handle %d = %q, decoded %d (%v)", n, id, got, ok)
		}
		seen[id] = true
		count, err := codec.Count(id)
		if err != nil || count > 4 {
			t.Fatalf("%q costs %d tokens: %v", id, count, err)
		}
		maxTokens = max(maxTokens, count)
	}
	t.Logf("first 4096 short handles use at most %d GPT-5 tokens", maxTokens)
	for _, n := range []uint64{999999, ^uint64(0)} {
		if got, ok := parseShortHandle(shortHandle(n)); !ok || got != n {
			t.Fatalf("large handle did not round trip: %d", n)
		}
	}
	for _, id := range []string{"", "../amber", "Amber", "amber0", "amber01", "amber-1", "amber99999999999999999999"} {
		if _, ok := parseShortHandle(id); ok {
			t.Errorf("accepted %q", id)
		}
	}
}

func TestShortHandleAllocationConcurrentRestart(t *testing.T) {
	directory := t.TempDir()
	var workers sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]bool)
	for range 8 {
		workers.Go(func() {
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Error(err)
				return
			}
			ids, err := store.allocateHandles(t.Context(), 8)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if seen[id] {
					t.Errorf("reused handle %q", id)
				}
				seen[id] = true
			}
		})
	}
	workers.Wait()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.allocateHandles(t.Context(), 1)
	if err != nil || len(ids) != 1 || ids[0] != shortHandle(64) {
		t.Fatalf("restart allocation: %v, %v", ids, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "handle-counter"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.allocateHandles(t.Context(), 1); err == nil {
		t.Fatal("corrupt counter silently reset")
	}
}

func TestShortHandlesRejectLegacyReferences(t *testing.T) {
	if validShellOutputID("r_AAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("accepted legacy read reference")
	}
	if _, err := expandChangeRefs([]string{"hp_a1"}); err == nil {
		t.Fatal("accepted legacy change reference")
	}
}

func TestShortReadCursorSurvivesRestart(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.putShellOutput(t.Context(), "output", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.readShellOutput(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.putReadCursor(t.Context(), source, [2]int{1, 0}, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.putReadCursor(t.Context(), source, [2]int{1, 0}, "stdout")
	if err != nil || cursor != again {
		t.Fatalf("cursor changed after restart: %q != %q, %v", cursor, again, err)
	}
}

func TestShortReadReferencesInInheritedJSON(t *testing.T) {
	for _, source := range []string{
		"hread maple",
		"echo before\nhread maple",
		"echo before\r\nhread\tmaple",
		"read: incomplete; next_call: hread maple",
	} {
		raw := string(mustMarshalJSON(source))
		matches := retainedReadReference.FindAllStringSubmatch(raw, -1)
		if len(matches) != 1 || matches[0][1] != "maple" {
			t.Fatalf("lost inherited read in %q: %v", raw, matches)
		}
	}
	if matches := retainedReadReference.FindAllString("maple trees by the lake", -1); len(matches) != 0 {
		t.Fatalf("ordinary prose claimed output records: %v", matches)
	}
}
