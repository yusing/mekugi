package executionobservation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestShortHandlesRoundTripAndTokenCost(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	maxTokens := 0
	seen := make(map[string]bool)
	for n := range uint64(4096) {
		id := history.ShortHandle(n)
		got, ok := history.ParseHandle(id)
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
		if got, ok := history.ParseHandle(history.ShortHandle(n)); !ok || got != n {
			t.Fatalf("large handle did not round trip: %d", n)
		}
	}
	for _, id := range []string{"", "../amber", "Amber", "amber0", "amber01", "amber-1", "amber99999999999999999999"} {
		if _, ok := history.ParseHandle(id); ok {
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
			ids, err := func() ([]string, error) {
				session, err := store.recoverySession(t.Context())
				if err != nil {
					return nil, err
				}
				return session.Reserve(t.Context(), 8)
			}()
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
	session, err := store.recoverySession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := session.Reserve(t.Context(), 1)
	if err != nil || len(ids) != 1 || ids[0] != history.ShortHandle(64) {
		t.Fatalf("restart allocation: %v, %v", ids, err)
	}
	if err := os.WriteFile(filepath.Join(directory, history.ScopeName("")), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reserve(t.Context(), 1); err == nil {
		t.Fatal("corrupt counter silently reset")
	}
}

func TestShortHandlesRejectLegacyReferences(t *testing.T) {
	if _, ok := history.ParseHandle("r_AAAAAAAAAAAAAAAAAAAAAA"); ok {
		t.Fatal("accepted legacy read reference")
	}
	if _, _, err := changes.ParseID("hp_a1"); err == nil {
		t.Fatal("accepted legacy change reference")
	}
}

func TestShortReadReferencesInInheritedJSON(t *testing.T) {
	for _, source := range []string{
		"mread maple",
		"echo before\nmread maple",
		"echo before\r\nmread\tmaple",
		"read: incomplete; next_call: mread maple",
	} {
		raw := string(responses.MustMarshalJSON(source))
		matches := retainedReadReference.FindAllStringSubmatch(raw, -1)
		if len(matches) != 1 || matches[0][1] != "maple" {
			t.Fatalf("lost inherited read in %q: %v", raw, matches)
		}
	}
	if matches := retainedReadReference.FindAllString("maple trees by the lake", -1); len(matches) != 0 {
		t.Fatalf("ordinary prose claimed output records: %v", matches)
	}
}
