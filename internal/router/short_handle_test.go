package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestShortRecoveryHandlesSurviveRestartAndRejectStale(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	first, err := transform.translate("original", "new bad.go\ntype \"package p\\nvar =\\n\"\n", nil)
	if err != nil || !first.EvaluatorRejected || len(first.RecoveryHandles) != 2 {
		t.Fatalf("rejection: %+v, %v", first, err)
	}
	handle := first.RecoveryHandles[1]
	if _, ok := parseShortHandle(handle); !ok || !strings.Contains(first.TranslationError, handle+" value VALUE") {
		t.Fatalf("missing short handle: %s", first.TranslationError)
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"original": first}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	retained, found, err := reopened.lookup(t.Context(), workspace, "original")
	if err != nil || !found || !slices.Equal(first.RecoveryHandles, retained.RecoveryHandles) {
		t.Fatalf("replay mapping changed: %+v, %v", retained, err)
	}
	fresh, freshProxy, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	freshProxy.replayStore = reopened
	fresh.directory = workspace
	fresh.visible = map[string]mekugiHistory{"original": retained}
	rejected, err := fresh.translateRecovery("again", handle+` value "package p\nvar ?\n"`, nil)
	if err != nil || !rejected.EvaluatorRejected || slices.Equal(rejected.RecoveryHandles, first.RecoveryHandles) {
		t.Fatalf("re-rejection did not refresh handles: %+v, %v", rejected, err)
	}
	stale, err := fresh.translateRecovery("stale", handle+` value "package p\n"`, nil)
	if err != nil || !stale.Unevaluated || !strings.Contains(stale.TranslationError, "stale") {
		t.Fatalf("old baseline accepted: %+v, %v", stale, err)
	}
	fixed, err := fresh.translateRecovery("fixed", rejected.RecoveryHandles[1]+` value "package p\n"`, nil)
	if err != nil || fixed.TranslationError != "" || fixed.CorrelationID != first.CorrelationID {
		t.Fatalf("restarted recovery: %+v, %v", fixed, err)
	}
}

func TestShortHandlesRejectLegacyReferences(t *testing.T) {
	if validShellOutputID("r_AAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("accepted legacy read reference")
	}
	if _, err := expandChangeRefs([]string{"hp_a1"}); err == nil {
		t.Fatal("accepted legacy change reference")
	}
	if _, err := mixedArtifactName("M" + strings.Repeat("a", 32)); err == nil {
		t.Fatal("accepted legacy resume handle")
	}
}

func TestRecoveryRejectsUnboundLegacyBaseline(t *testing.T) {
	history := mekugiHistory{
		ToolName: mekugiToolName, Script: "in f.txt\ntype 1:aaaa \"new\"\n",
		EvaluatorRejected: true, TranslationError: "rejected",
	}
	if _, err := recoveryHistoryOf(slices.Values([]mekugiHistory{history})); err == nil || !strings.Contains(err.Error(), "binding") {
		t.Fatalf("accepted unbound legacy baseline: %v", err)
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

func TestShortRecoveryRejectsChangedRetainedBinding(t *testing.T) {
	for _, mutation := range []string{"path", "mapping"} {
		t.Run(mutation, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			baseline := "in original.txt\ntype 1:aaaa \"value\"\n"
			handles, err := store.allocateHandles(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			history := mekugiHistory{
				ToolName: "hpatch", Script: baseline, Root: "/w",
				EvaluatorRejected: true, TranslationError: "rejected",
				RecoveryHandles: handles, RecoveryBinding: recoveryHandlesBinding(baseline, handles),
			}
			if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"original": history}); err != nil {
				t.Fatal(err)
			}
			if mutation == "path" {
				history.Evaluated = strings.Replace(baseline, "original.txt", "different.txt", 1)
			} else {
				history.RecoveryHandles = []string{handles[1], handles[0]}
			}
			path := filepath.Join(store.directory, replayRecordName("/w", "original", false))
			if err := os.WriteFile(path, mustMarshalJSON(replayRecord{
				Version: 1, Workspace: "/w", CallID: "original", History: history,
			}), 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			retained, found, err := reopened.lookup(t.Context(), "/w", "original")
			if err != nil || !found {
				t.Fatalf("read damaged fixture: %v, %v", found, err)
			}
			if _, err := recoveryHistoryOf(slices.Values([]mekugiHistory{retained})); err == nil ||
				!strings.Contains(err.Error(), "binding") {
				t.Fatalf("accepted altered retained %s: %v", mutation, err)
			}
		})
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
