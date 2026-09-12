package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJournalAtomicMutationsAndReplay(t *testing.T) {
	store := newJournalStore()
	ctx := t.Context()
	if err := store.initialize(ctx, nil, "/workspace", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	ids, err := store.apply(ctx, nil, "/workspace", "root", "call", []journalMutation{{Op: "add", Text: new("Checked the request boundary")}})
	if err != nil || len(ids) != 1 || ids[0] != "j1" {
		t.Fatalf("add: %v %v", ids, err)
	}
	replayed, err := store.apply(ctx, nil, "/workspace", "root", "call", []journalMutation{{Op: "add", Text: new("Checked the request boundary")}})
	if err != nil || len(replayed) != 1 || replayed[0] != "j1" {
		t.Fatalf("replay: %v %v", replayed, err)
	}
	if _, err := store.apply(ctx, nil, "/workspace", "root", "call", []journalMutation{{Op: "add", Text: new("Changed")}}); err == nil {
		t.Fatal("accepted changed call")
	}
	if _, err := store.apply(ctx, nil, "/workspace", "root", "bad", []journalMutation{{Op: "edit", ID: "j1", Text: new("Must roll back")}, {Op: "delete", ID: "j99"}}); err == nil {
		t.Fatal("accepted invalid batch")
	}
	items, err := store.list(ctx, nil, "/workspace", "root")
	if err != nil || len(items) != 1 || items[0].Text != "Checked the request boundary" {
		t.Fatalf("atomicity: %+v %v", items, err)
	}
	ids, err = store.apply(ctx, nil, "/workspace", "root", "next", []journalMutation{{Op: "delete", ID: "j1"}, {Op: "add", Text: new("Verified")}})
	if err != nil || ids[1] != "j2" {
		t.Fatalf("stable IDs: %v %v", ids, err)
	}
}

func TestJournalRestartAndIndependentLatestFork(t *testing.T) {
	ctx := t.Context()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newJournalStore()
	if err := store.initialize(ctx, replay, "/workspace", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(ctx, replay, "/workspace", "root", "a", []journalMutation{{Op: "add", Text: new("First")}}); err != nil {
		t.Fatal(err)
	}
	store = newJournalStore()
	if _, err := store.apply(ctx, replay, "/workspace", "root", "b", []journalMutation{{Op: "edit", ID: "j1", Text: new("Latest")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.initialize(ctx, replay, "/workspace", "fork", "/root", "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(ctx, replay, "/workspace", "root", "c", []journalMutation{{Op: "edit", ID: "j1", Text: new("After fork")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.initialize(ctx, replay, "/workspace", "fork", "/root", "root"); err != nil {
		t.Fatal(err)
	}
	items, err := store.list(ctx, replay, "/workspace", "fork")
	if err != nil || len(items) != 1 || items[0].Text != "Latest" {
		t.Fatalf("fork: %+v %v", items, err)
	}
	if _, err := store.list(ctx, replay, "/other", "root"); err == nil {
		t.Fatal("journal leaked across workspaces")
	}
}

func TestJournalConcurrentAddsAndCapacity(t *testing.T) {
	store := newJournalStore()
	ctx := t.Context()
	if err := store.initialize(ctx, nil, "", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range maxJournalItems {
		workers.Go(func() {
			if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "add", Text: new("Milestone")}}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "add", Text: new("Overflow")}}); err == nil {
		t.Fatal("capacity admitted another item")
	}
	items, err := store.list(ctx, nil, "", "root")
	if err != nil || len(items) != maxJournalItems {
		t.Fatalf("items: %d %v", len(items), err)
	}
	seen := make(map[string]bool)
	for _, item := range items {
		if seen[item.ID] {
			t.Fatalf("duplicate ID: %s", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestJournalMutationValidation(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[{"op":"add","extra":true}]`, `[{"op":"add"}] trailing`} {
		if _, err := decodeJournalMutations([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	store := newJournalStore()
	if err := store.initialize(t.Context(), nil, "", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []journalMutation{
		{Op: "add"}, {Op: "add", Text: new(" \n")}, {Op: "add", ID: "j1", Text: new("body")},
		{Op: "add", Text: new(strings.Repeat("x", maxJournalItemBytes+1))},
		{Op: "edit", ID: "j1", Text: new("missing")}, {Op: "delete", ID: "j1"}, {Op: "unknown"},
	} {
		if _, err := store.apply(t.Context(), nil, "", "root", "", []journalMutation{mutation}); err == nil {
			t.Errorf("accepted %+v", mutation)
		}
	}
}

func TestJournalAcknowledgesOnlyRenderedRevision(t *testing.T) {
	ctx := t.Context()
	store := newJournalStore()
	if err := store.initialize(ctx, nil, "", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "add", Text: new("Before"), ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.list(ctx, nil, "", "root")
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "edit", ID: "j1", Text: new("After")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.acknowledge(ctx, nil, "", "root", map[string]uint64{"j1": before[0].Updated}, true); err != nil {
		t.Fatal(err)
	}
	after, _ := store.list(ctx, nil, "", "root")
	if after[0].Reported || after[0].Flushed || !after[0].EverReported {
		t.Fatalf("revision acknowledgement: %+v", after[0])
	}
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "delete", ID: "j1", ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	j := store.memory[journalKey("", "root")]
	if len(j.Retractions) != 1 || len(j.Items) != 0 {
		t.Fatalf("retraction: %+v", j)
	}
	if err := store.acknowledge(ctx, nil, "", "root", map[string]uint64{"j1": j.Retractions[0].Sequence}, false); err != nil {
		t.Fatal(err)
	}
	if len(store.memory[journalKey("", "root")].Retractions) != 0 {
		t.Fatal("retraction not acknowledged")
	}
}

func TestJournalRejectsCorruptReceiptMap(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(replay.directory, journalFilename("", "root"))
	for _, suffix := range []string{`}`, `,"receipts":null}`} {
		data := []byte(`{"version":1,"workspace":"","thread":"root"` + suffix)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newJournalStore()
		if _, err := store.apply(t.Context(), replay, "", "root", "call", []journalMutation{{Op: "add", Text: new("body")}}); err == nil {
			t.Fatal("accepted corrupt receipt map")
		}
	}
}

func TestJournalDeletionWaitsForDelivery(t *testing.T) {
	ctx := t.Context()
	store := newJournalStore()
	if err := store.initialize(ctx, nil, "", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "add", Text: new("Notice")}}); err != nil {
		t.Fatal(err)
	}
	release, err := store.lockDelivery(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation proves a mutation cannot pass the delivery lease without
	// depending on scheduling or a sleep.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.apply(cancelled, nil, "", "root", "", []journalMutation{{Op: "delete", ID: "j1", ReportNow: true}}); err == nil {
		t.Fatal("delete overtook delivery")
	}
	if err := store.acknowledge(ctx, nil, "", "root", map[string]uint64{"j1": 1}, false); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := store.apply(ctx, nil, "", "root", "", []journalMutation{{Op: "delete", ID: "j1", ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	if len(store.memory[journalKey("", "root")].Retractions) != 1 {
		t.Fatal("displayed deletion lost its retraction")
	}
}

func TestJournalDeliverySerializesIndependentStores(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, second := newJournalStore(), newJournalStore()
	ctx := t.Context()
	if err := first.initialize(ctx, replay, "", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := first.apply(ctx, replay, "", "root", "", []journalMutation{{Op: "add", Text: new("Notice")}}); err != nil {
		t.Fatal(err)
	}
	release, err := first.lockDelivery(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := second.apply(waiting, replay, "", "root", "", []journalMutation{{Op: "delete", ID: "j1", ReportNow: true}}); !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("independent store did not wait for delivery: %v", err)
	}
	if err := first.acknowledge(ctx, replay, "", "root", map[string]uint64{"j1": 1}, false); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if _, err := second.apply(ctx, replay, "", "root", "", []journalMutation{{Op: "delete", ID: "j1", ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	journal, _, err := readThreadJournal(replay, "", "root")
	if err != nil || len(journal.Retractions) != 1 {
		t.Fatalf("cross-store retraction lost: %+v %v", journal.Retractions, err)
	}
}

func TestJournalStateWaitHonorsCancellation(t *testing.T) {
	for _, operation := range []string{"transaction", "list", "delivery"} {
		t.Run(operation, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			release, err := proxy.journals.lockState(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				switch operation {
				case "transaction":
					result <- proxy.journals.initialize(ctx, proxy.replayStore, transform.directory, "another", "/root", "")
				case "list":
					_, err := proxy.journals.list(ctx, proxy.replayStore, transform.directory, transform.shellThreadID)
					result <- err
				case "delivery":
					transform.ctx = ctx
					_, err := transform.prepareJournalDelivery(true)
					result <- err
				}
			}()
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled state wait = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled caller remained blocked on journal state")
			}
			if transform.journalDeliveryRelease != nil {
				t.Fatal("canceled state wait retained the delivery lease")
			}
		})
	}
}

func TestJournalDiskCapacityOnlyBlocksNewThreads(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := newJournalStore()
	for index := range maxJournalThreads {
		if err := writer.initialize(t.Context(), replay, "/workspace", fmt.Sprint(index), "/root", ""); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh store must see journals created by another router, without a cache.
	reader := newJournalStore()
	if err := reader.initialize(t.Context(), replay, "/workspace", "overflow", "/root", ""); !errors.Is(err, errJournalThreadCapacity) {
		t.Fatalf("new journal at capacity = %v", err)
	}
	if _, err := reader.apply(t.Context(), replay, "/workspace", "0", "", []journalMutation{{Op: "add", Text: new("Updated at capacity")}}); err != nil {
		t.Fatalf("existing journal update at capacity = %v", err)
	}
}
