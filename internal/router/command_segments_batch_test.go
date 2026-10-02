package router

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func batchCommandSegmentsFixture(id string) (appServerItem, *retainedCommandSegments) {
	item := appServerItem{ID: id, Type: "commandExecution", Status: "completed", Command: "bash -lc 'echo one; false'", AggregatedOutput: new("one\n"), ExitCode: new(1)}
	record := &retainedCommandSegments{
		Command: item.Command, Exit: 1, Output: sha256.Sum256([]byte(*item.AggregatedOutput)),
		Parts: []retainedCommandSegment{{Source: "echo one", Output: new("one")}, {Source: "false", Exit: 1, Output: new("")}},
	}
	return item, record
}

func TestCommandSegmentsBatchMixedCandidates(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	turn := appServerHistoryTurn{ID: "turn", Status: "completed"}
	histories := make(map[string]mekugiHistory)
	valid := make(map[string]bool)
	// Span multiple bounded batches, with invalid records among valid ones.
	for i := range 150 {
		item, record := batchCommandSegmentsFixture(fmt.Sprintf("item-%03d", i))
		valid[item.ID] = i%16 == 0 || i%16 == 15
		switch i % 16 {
		case 1: // A genuinely missing record, not a malformed envelope.
			record = nil
		case 2:
			record.Command = "bash -lc 'echo other; false'"
		case 3:
			record.Output = sha256.Sum256([]byte("different"))
		case 4:
			record.Exit = 0
		case 5:
			record.Parts = record.Parts[:1]
		case 6:
			record.Parts[0].Source = "echo other"
		case 7:
			record.Parts[1].Skipped = true // Skipped observations cannot contain output or an exit.
		case 8:
			record.Parts[0].Timing.ElapsedNS = -1
		case 9:
			item.AggregatedOutput = nil
		case 10:
			item.ExitCode = nil
		case 11:
			item.Type = "agentMessage"
		case 12:
			record.Parts[0].Timing.Ended = time.Unix(1, 0)
		case 13:
			record.Parts[0].Timing.Started = time.Unix(1, 0)
			record.Parts[0].Timing.ElapsedNS = 1
		case 14:
			item.Command = "echo one"
		}
		turn.Items = append(turn.Items, item)
		if record != nil {
			histories[commandSegmentsID(turn.ID, item.ID)] = mekugiHistory{CommandSegments: record}
		}
	}
	corrupt, record := batchCommandSegmentsFixture("corrupt")
	turn.Items = slices.Insert(turn.Items, 1, corrupt)
	histories[commandSegmentsID(turn.ID, corrupt.ID)] = mekugiHistory{CommandSegments: record}
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, replayRecordName(workspace, commandSegmentsID(turn.ID, corrupt.ID), false)), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	u := &appServerUI{ctx: t.Context(), thread: "unrelated-current-thread", proxy: &mekugiProxy{replayStore: store}}
	got := u.prepareCommandSegments("fork", workspace, turn)
	catalog, err := store.readRetainedSession(storageSessionName("fork"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range turn.Items {
		if (got[item.ID] != nil) != valid[item.ID] {
			t.Errorf("%s: accepted = %v, want %v", item.ID, got[item.ID] != nil, valid[item.ID])
		}
		name := replayRecordName(workspace, commandSegmentsID(turn.ID, item.ID), false)
		if catalog.Files[name] != valid[item.ID] {
			t.Errorf("%s: durable ownership = %v, want %v", item.ID, catalog.Files[name], valid[item.ID])
		}
	}
	if _, err := store.readRetainedSession(storageSessionName(u.thread)); !os.IsNotExist(err) {
		t.Fatalf("adopted records under the current UI thread instead of the supplied stable thread: %v", err)
	}
	if got := u.prepareCommandSegments("other", filepath.Join(workspace, "other"), turn); len(got) != 0 {
		t.Fatal("batch borrowed another workspace's reports")
	}
	turn.ID = "other-turn"
	if got := u.prepareCommandSegments("other", workspace, turn); len(got) != 0 {
		t.Fatal("batch borrowed another turn's reports")
	}
}

func TestCommandSegmentsBatchRejectsUnretainedRecords(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, record := batchCommandSegmentsFixture("item")
	turn := appServerHistoryTurn{ID: "turn", Items: []appServerItem{item}}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{commandSegmentsID(turn.ID, item.ID): {CommandSegments: record}}); err != nil {
		t.Fatal(err)
	}
	// Reading is possible, but ownership cannot be persisted for this target.
	if err := os.WriteFile(filepath.Join(store.directory, storageSessionName("fork")), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	u := &appServerUI{ctx: t.Context(), proxy: &mekugiProxy{replayStore: store}}
	if got := u.prepareCommandSegments("fork", workspace, turn); got[item.ID] != nil {
		t.Fatal("published a record without durable target ownership")
	}
}

func TestCommandSegmentsBatchForkOwnershipSurvivesParentExpiry(t *testing.T) {
	for _, consumer := range []string{"main", "activity"} {
		t.Run(consumer, func(t *testing.T) {
			workspace, directory := t.TempDir(), t.TempDir()
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			parent, release := retentionTestSession(t, store, "parent", 0)
			item, record := batchCommandSegmentsFixture("item")
			turn := appServerHistoryTurn{ID: "turn", Status: "completed", Items: []appServerItem{item}}
			id := commandSegmentsID(turn.ID, item.ID)
			if err := store.put(parent, workspace, map[string]mekugiHistory{id: {CommandSegments: record}}); err != nil {
				t.Fatal(err)
			}
			release()
			u := newAppServerSessionTestUI(t, workspace)
			u.proxy = &mekugiProxy{replayStore: store}
			view := u.view
			if consumer == "main" {
				u.thread = "fork"
				u.restoreHistory([]appServerHistoryTurn{turn})
			} else {
				u.session.path("fork")
				u.restoreActivityThread(appServerThreadInfo{ID: "fork", Cwd: workspace, Turns: []appServerHistoryTurn{turn}})
				view = u.agents
			}
			var restored *liveActivityNativeItem
			for _, entry := range view.entries {
				if entry.CallID == item.ID {
					restored = entry.native
				}
			}
			if restored == nil || len(restored.segments) != 2 || strings.Join(restored.segments[0].output.View().Lines, "\n") != "one" {
				t.Fatal("consumer did not restore per-command evidence")
			}
			if u.restoredSegments != nil {
				t.Fatal("turn-local prepared records leaked beyond history restoration")
			}
			retentionTestAge(t, store, "parent", 30*24*time.Hour)
			store, err = openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			current, _ := retentionTestSession(t, store, "current", 0)
			if err := store.cleanupSessions(current); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(directory, storageSessionName("parent"))); !os.IsNotExist(err) {
				t.Fatalf("parent did not expire: %v", err)
			}
			u.proxy.replayStore = store
			if got := u.prepareCommandSegments("fork", workspace, turn); got[item.ID] == nil {
				t.Fatal("fork lost its inherited report after reopen and parent expiry")
			}
		})
	}
}

func BenchmarkCommandSegmentsBatchLargeHistory(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("items-%d", count), func(b *testing.B) {
			workspace := b.TempDir()
			store, err := openMekugiReplayStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			turn := appServerHistoryTurn{ID: "turn", Status: "completed"}
			histories := make(map[string]mekugiHistory, count)
			for i := range count {
				item, record := batchCommandSegmentsFixture(fmt.Sprintf("item-%d", i))
				turn.Items = append(turn.Items, item)
				histories[commandSegmentsID(turn.ID, item.ID)] = mekugiHistory{CommandSegments: record}
			}
			if err := store.put(b.Context(), workspace, histories); err != nil {
				b.Fatal(err)
			}
			u := &appServerUI{ctx: b.Context(), proxy: &mekugiProxy{replayStore: store}}
			b.ReportAllocs()
			iteration := 0
			for b.Loop() {
				// A new owner each time measures adoption, not only warm catalog reads.
				got := u.prepareCommandSegments(fmt.Sprintf("fork-%d", iteration), workspace, turn)
				iteration++
				if len(got) != count {
					b.Fatalf("restored %d/%d records", len(got), count)
				}
			}
		})
	}
}
