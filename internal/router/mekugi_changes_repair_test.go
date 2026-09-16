package router

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTrackedConfirmationRepairsInterruptedPublication(t *testing.T) {
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "thread", "first")
	if err != nil {
		t.Fatal(err)
	}
	first := mekugiHistory{ChangeID: id, CorrelationID: "first", Attempt: 1, sequence: 1, TranslationError: "rejected"}
	middle := mekugiHistory{ChangeID: id, CorrelationID: "first", Attempt: 2, sequence: 2, Report: changeNotice(id) + "success\n"}
	last := mekugiHistory{ChangeID: id, CorrelationID: "first", Attempt: 3, sequence: 3, TranslationError: "blocked"}
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"first": first, "last": last}); err != nil {
		t.Fatal(err)
	}
	// Persist only the replay record, representing interruption between the
	// replay write and index publication. A later visible call is already indexed.
	if err := store.locked(t.Context(), func() error {
		return store.write(replayRecord{Version: 1, Workspace: "/w", CallID: "middle", History: durableHistory(middle)})
	}); err != nil {
		t.Fatal(err)
	}
	store, err = openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	restored, found, err := store.lookup(t.Context(), "/w", "middle")
	if err != nil || !found {
		t.Fatalf("replay = %v, %v", found, err)
	}
	restored.confirmed = true
	for range 2 {
		if err := store.confirmChanges(t.Context(), "/w", map[string]mekugiHistory{"middle": restored}); err != nil {
			t.Fatal(err)
		}
		index, err := store.readChangeIndex("/w")
		want := []trackedCall{{ID: "first"}, {ID: "middle", Confirmed: true}, {ID: "last"}}
		if err != nil || !reflect.DeepEqual(index.Changes[id].Calls, want) {
			t.Fatalf("repaired calls = %#v, %v", index.Changes[id].Calls, err)
		}
	}
	// A normal publication retry also preserves the repaired receipt and order.
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"middle": middle}); err != nil {
		t.Fatal(err)
	}
	text, err := store.readChanges(t.Context(), changeReadOptions{workspace: "/w", ids: []string{id}})
	if err != nil || !strings.Contains(text, "attempts=3\nattempt 1 rejected\nattempt 2 applied\nattempt 3 rejected\n") {
		t.Fatalf("read = %q, %v", text, err)
	}
}

func TestTrackedConfirmationRejectsUnrepairablePublication(t *testing.T) {
	for _, failure := range []string{"missing", "wrong change", "wrong correlation", "invalid attempt", "rejected", "index correlation"} {
		t.Run(failure, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id, err := store.reserveChange(t.Context(), "/w", "thread", "call")
			if err != nil {
				t.Fatal(err)
			}
			history := mekugiHistory{ChangeID: id, CorrelationID: "call", Attempt: 1, confirmed: true}
			record := replayRecord{Version: 1, Workspace: "/w", CallID: "call", History: durableHistory(history)}
			switch failure {
			case "wrong change":
				record.History.ChangeID = "amber2"
			case "wrong correlation":
				record.History.CorrelationID = "other"
			case "invalid attempt":
				record.History.Attempt = 0
			case "rejected":
				record.History.TranslationError = "rejected"
			case "index correlation":
				history.CorrelationID = "other"
			}
			if failure != "missing" {
				if err := store.locked(t.Context(), func() error { return store.write(record) }); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(store.directory, changeIndexName("/w"))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.confirmChanges(t.Context(), "/w", map[string]mekugiHistory{"call": history}); err == nil {
				t.Fatal("accepted inconsistent confirmation")
			}
			after, err := os.ReadFile(path)
			if err != nil || !slices.Equal(before, after) {
				t.Fatal("failed repair changed the index")
			}
		})
	}
}

func TestTrackedReadUsesAnUnlockedSnapshot(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "thread", "call")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: "call", Attempt: 1, Report: "success\n"}
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"call": history}); err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: "/w", ids: []string{id}}
	snapshot, err := store.readChangeIndex("/w")
	if err != nil {
		t.Fatal(err)
	}
	history.confirmed = true
	if err := store.confirmChanges(t.Context(), "/w", map[string]mekugiHistory{"call": history}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Rendering a previously selected snapshot neither locks out the writer nor
	// picks up a receipt published after that snapshot.
	if err := store.locked(ctx, func() error {
		text, err := store.renderChanges(ctx, options, snapshot)
		if err != nil || !strings.Contains(text, "application unconfirmed") {
			t.Fatalf("snapshot read = %q, %v", text, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.readChanges(t.Context(), options)
	if err != nil || !strings.Contains(current, "amber1 applied") {
		t.Fatalf("current read = %q, %v", current, err)
	}
}
