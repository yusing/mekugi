package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func retentionTestSession(t *testing.T, store *mekugiReplayStore, thread string, age time.Duration) (context.Context, func()) {
	t.Helper()
	ctx, release, err := store.beginSession(t.Context(), thread, thread)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if age > 0 {
		retentionTestAge(t, store, thread, age)
	}
	return ctx, release
}

func retentionTestAge(t *testing.T, store *mekugiReplayStore, thread string, age time.Duration) {
	t.Helper()
	if err := store.locked(t.Context(), func() error {
		record, err := store.readRetainedSession(storageSessionName(thread))
		if err != nil {
			return err
		}
		record.LastUsed = time.Now().Add(-age)
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return store.writeFile(storageSessionName(thread), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
}

func retentionTestPut(t *testing.T, store *mekugiReplayStore, ctx context.Context, workspace, call string) {
	t.Helper()
	if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {ToolName: "shell", Script: "true"}}); err != nil {
		t.Fatal(err)
	}
}

func retentionTestExists(t *testing.T, store *mekugiReplayStore, workspace, call string, want bool) {
	t.Helper()
	_, found, err := store.lookup(t.Context(), workspace, call)
	if err != nil || found != want {
		t.Fatalf("record %s: found=%v want=%v err=%v", call, found, want, err)
	}
}

func TestStorageRetentionExpiresInactiveSessionsOnly(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, releaseOld := retentionTestSession(t, store, "old", 0)
	retentionTestPut(t, store, old, "/w", "old-call")
	releaseOld()
	retentionTestAge(t, store, "old", 15*24*time.Hour)
	recent, releaseRecent := retentionTestSession(t, store, "recent", 0)
	retentionTestPut(t, store, recent, "/w", "recent-call")
	releaseRecent()
	retentionTestAge(t, store, "recent", 13*24*time.Hour)
	active, _ := retentionTestSession(t, store, "active", 0)
	retentionTestPut(t, store, active, "/other", "active-call")
	retentionTestAge(t, store, "active", 30*24*time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	var notices []string
	store.storageNotice = func(_ string, message string) { notices = append(notices, message) }
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	retentionTestExists(t, store, "/w", "old-call", false)
	retentionTestExists(t, store, "/w", "recent-call", true)
	retentionTestExists(t, store, "/other", "active-call", true)
	if len(notices) != 1 || !strings.Contains(notices[0], "Codex chats are unchanged") {
		t.Fatalf("cleanup notice = %v", notices)
	}
}

func TestStoragePressureRemovesOldestInactiveSessionAcrossWorkspaces(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, releaseOld := retentionTestSession(t, store, "old", 0)
	retentionTestPut(t, store, old, "/first", "old")
	releaseOld()
	retentionTestAge(t, store, "old", 2*time.Hour)
	newer, releaseNewer := retentionTestSession(t, store, "newer", 0)
	retentionTestPut(t, store, newer, "/second", "newer")
	releaseNewer()
	retentionTestAge(t, store, "newer", time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	retentionTestPut(t, store, current, "/third", "new")
	retentionTestExists(t, store, "/first", "old", false)
	retentionTestExists(t, store, "/second", "newer", true)
	retentionTestExists(t, store, "/third", "new", true)
}

func TestStoragePressurePreservesRunningWorkAndReportsLimit(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active, _ := retentionTestSession(t, store, "active", 0)
	retentionTestPut(t, store, active, "/w", "active")
	retentionTestAge(t, store, "active", time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	err = store.put(current, "/w", map[string]mekugiHistory{"new": {Script: "true"}})
	if err == nil || !strings.Contains(err.Error(), "limit is") || !strings.Contains(err.Error(), "Running sessions") {
		t.Fatalf("storage pressure error = %v", err)
	}
	retentionTestExists(t, store, "/w", "active", true)
	retentionTestExists(t, store, "/w", "new", false)
}

func TestStorageRetentionForkOwnershipSurvivesRestart(t *testing.T) {
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	retentionTestPut(t, store, parent, "/w", "shared")
	releaseParent()
	child, releaseChild := retentionTestSession(t, store, "child", 0)
	history, found, err := store.lookup(child, "/w", "shared")
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := store.retainInput(child, "/w", nil, map[string]mekugiHistory{"shared": history}, nil); err != nil {
		t.Fatal(err)
	}
	releaseChild()
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	retentionTestAge(t, store, "child", time.Hour)
	restarted, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := retentionTestSession(t, restarted, "current", 0)
	if err := restarted.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	retentionTestExists(t, restarted, "/w", "shared", true)
	if _, err := os.Stat(filepath.Join(directory, storageSessionName("parent"))); !os.IsNotExist(err) {
		t.Fatalf("expired parent catalog remains: %v", err)
	}
}

func TestStorageRetentionPreservesReadSourceAndExpiresSessionOutputs(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	id, err := store.putShellOutput(parent, "a\nb\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.readShellOutput(parent, id)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.putReadCursor(parent, source, [2]int{2, 0}, "")
	if err != nil {
		t.Fatal(err)
	}
	releaseParent()
	child, releaseChild := retentionTestSession(t, store, "child", 0)
	child = bindTestHandleScope(t, store, child, "", "parent")
	if _, err := store.readShellOutput(child, cursor); err != nil {
		t.Fatal(err)
	}
	releaseChild()
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readShellOutput(child, id); err != nil {
		t.Fatalf("shared source lost: %v", err)
	}
}

func TestStorageRetentionDoesNotFollowSymlinksOrRemoveUnknownFiles(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "chat.json")
	if err := os.WriteFile(outside, []byte("original chat"), 0600); err != nil {
		t.Fatal(err)
	}
	name := replayRecordName("/w", "link", false)
	if err := os.Symlink(outside, filepath.Join(store.directory, name)); err != nil {
		t.Fatal(err)
	}
	ctx, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(ctx); err == nil {
		t.Fatal("cleanup followed a managed symlink")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "original chat" {
		t.Fatalf("outside data changed: %s %v", data, err)
	}
}

func TestStorageCleanupRetiresChangesWithoutReusingIDs(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, release := retentionTestSession(t, store, "old", 0)
	id, err := store.reserveChange(old, "/w", "old", "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(old, "/w", map[string]mekugiHistory{"call": {ChangeID: id, CorrelationID: "first", Attempt: 1}}); err != nil {
		t.Fatal(err)
	}
	release()
	retentionTestAge(t, store, "old", 30*24*time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	index, err := store.scoped(old).readChangeIndex("/w")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := index.Changes[id]; exists {
		t.Fatal("expired change remains")
	}
	resumed, _ := retentionTestSession(t, store, "old", 0)
	next, err := store.reserveChange(resumed, "/w", "old", "second")
	if err != nil || next != "amber2" {
		t.Fatalf("next change = %s, %v", next, err)
	}
}

func TestStorageChangeReadDependenciesSurviveOriginalSessionExpiry(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, release := retentionTestSession(t, store, "parent", 0)
	id, err := store.reserveChange(parent, "/w", "parent", "edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(parent, "/w", map[string]mekugiHistory{"edit-call": {
		ToolName: "hpatch", ChangeID: id, CorrelationID: "edit", Attempt: 1, Script: "original",
	}}); err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: "/w", ids: []string{id}, view: "history"}
	text, err := store.readChanges(parent, options)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := store.putChangeRead(parent, options, text, 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	child, _ := retentionTestSession(t, store, "child", 0)
	child = bindTestHandleScope(t, store, child, "", "parent")
	// This is the inherited hread path, without a preceding hchanges in child.
	record, err := store.readShellOutput(child, reference)
	if err != nil {
		t.Fatal(err)
	}
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.readSourceStreams(child, record)
	if err != nil || replayed.Stdout != text[1:] {
		t.Fatalf("inherited change read: %q, %v", replayed.Stdout, err)
	}
	retentionTestExists(t, store, "/w", "edit-call", true)
}

func TestStorageAdoptsLegacyJournalReceiptEvenAfterJournalClaim(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Pre-catalog writes have no context-bound thread ownership.
	journals := newJournalStore()
	if err := journals.initialize(t.Context(), store, "/w", "old", "/root", ""); err != nil {
		t.Fatal(err)
	}
	retentionTestPut(t, store, t.Context(), "/w", "compacted-call")
	if _, err := journals.apply(t.Context(), store, "/w", "old", "compacted-call", []journalMutation{{Op: "add", Text: new("old milestone")}}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	for _, name := range []string{journalFilename("/w", "old"), replayRecordName("/w", "compacted-call", false)} {
		if err := os.Chtimes(filepath.Join(store.directory, name), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	resumed, _ := retentionTestSession(t, store, "old", 0)
	if err := journals.initialize(resumed, store, "/w", "old", "/root", ""); err != nil {
		t.Fatal(err)
	}
	current, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	retentionTestExists(t, store, "/w", "compacted-call", true)
}

func TestStorageCatalogGrowthCountsAgainstQuota(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := retentionTestSession(t, store, "parent", 0)
	retentionTestPut(t, store, parent, "/w", "shared")
	child, _ := retentionTestSession(t, store, "child", 0)
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	history, _, err := store.lookup(child, "/w", "shared")
	if err != nil {
		t.Fatal(err)
	}
	err = store.retainInput(child, "/w", nil, map[string]mekugiHistory{"shared": history}, nil)
	if err == nil || !strings.Contains(err.Error(), "limit is") {
		t.Fatalf("catalog quota was not enforced: %v", err)
	}
	catalog, err := store.readRetainedSession(storageSessionName("child"))
	if err != nil || len(catalog.Files) != 0 {
		t.Fatalf("failed catalog growth was not rolled back: %+v %v", catalog, err)
	}
	retentionTestExists(t, store, "/w", "shared", true)
}

func TestStorageSnapshotLeaseBridgesForkValidationAndAdoption(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	retentionTestPut(t, store, parent, "/w", "shared")
	releaseParent()
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	child, _ := retentionTestSession(t, store, "child", 0)
	releaseSnapshot, err := store.lockStorageSnapshot(child)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSnapshot()
	history, found, err := store.lookup(child, "/w", "shared")
	if err != nil || !found {
		t.Fatal(err)
	}
	// A second store has independent Go state but the same cross-process lock.
	other, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	otherCtx, _ := retentionTestSession(t, other, "other", 0)
	if err := other.cleanupSessions(otherCtx); err != nil {
		t.Fatal(err)
	}
	retentionTestExists(t, store, "/w", "shared", true)
	if err := store.retainInput(child, "/w", nil, map[string]mekugiHistory{"shared": history}, releaseSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := other.locked(otherCtx, func() error { return other.scoped(otherCtx).maintainStorage("", 0, true, nil) }); err != nil {
		t.Fatal(err)
	}
	retentionTestExists(t, store, "/w", "shared", true)
}

func TestStorageIndexLimitReclaimsInactiveChanges(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, release := retentionTestSession(t, store, "old", 0)
	correlation := strings.Repeat("x", maxReplayRecordBytes-2048)
	id, err := store.reserveChange(old, "/w", "old", correlation)
	if err != nil {
		t.Fatal(err)
	}
	// A reservation alone is still session-owned and reclaimable.
	release()
	retentionTestAge(t, store, "old", time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	current = bindTestHandleScope(t, store, current, "old", "")
	next, err := store.reserveChange(current, "/w", "current", strings.Repeat("y", 4096))
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.scoped(current).readChangeIndex("/w")
	if err != nil || next != "apple1" || index.Streams[0].Retired != 1 || len(index.Changes) != 1 || index.Changes[next].Correlation != strings.Repeat("y", 4096) {
		t.Fatalf("index reclamation: next=%s retired=%+v err=%v", next, index.Streams, err)
	}
	if _, exists := index.Changes[id]; exists {
		t.Fatal("inactive reservation survived index pressure")
	}
}

func TestStorageMaintenanceReusesUnchangedIndexEncoding(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(strconv.FormatBool(cleanup), func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if cleanup {
				old, release := retentionTestSession(t, store, "old", 0)
				retentionTestPut(t, store, old, "/other", "unrelated")
				release()
				retentionTestAge(t, store, "old", 30*24*time.Hour)
			}
			current, _ := retentionTestSession(t, store, "current", 0)
			if _, err := store.reserveChange(current, "/w", "current", "new"); err != nil {
				t.Fatal(err)
			}
			if err := store.locked(current, func() error {
				index, err := store.scoped(current).readChangeIndex("/w")
				if err != nil {
					return err
				}
				data, err := marshalProtocolJSON(index)
				if err != nil {
					return err
				}
				pending := pendingChangeIndexWrite{index: index, data: data}
				if err := store.scoped(current).maintainStorage(changeIndexName("/w", index.Namespace), int64(len(data)), cleanup, &pending); err != nil {
					return err
				}
				if len(pending.data) != len(data) || &pending.data[0] != &data[0] {
					t.Fatal("maintenance encoded an unchanged index again")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if cleanup {
				retentionTestExists(t, store, "/other", "unrelated", false)
			}
		})
	}
}

func TestStorageIndexPressurePersistsRetiredAttempts(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, release := retentionTestSession(t, store, "old", 0)
	id, err := store.reserveChange(old, "/w", "old", "original")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(old, "/w", map[string]mekugiHistory{
		"old-call": {ChangeID: id, CorrelationID: "original", Attempt: 1, Script: strings.Repeat("x", 2048)},
	}); err != nil {
		t.Fatal(err)
	}
	release()
	retentionTestAge(t, store, "old", time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	current = bindTestHandleScope(t, store, current, "old", "")
	if err := store.put(current, "/w", map[string]mekugiHistory{
		"current-call": {ChangeID: id, CorrelationID: "original", Attempt: 2, ExecutingThread: "current"},
	}); err != nil {
		t.Fatal(err)
	}
	sizes, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes = 0
	for _, size := range sizes {
		store.maxBytes += size
	}
	// Adding the reservation exceeds the exact current usage. Cleanup must
	// remove the old attempt, preserve the active attempt, and persist both
	// that retirement and the newly reserved ID in the replacement index.
	correlation := strings.Repeat("y", 1024)
	next, err := store.reserveChange(current, "/w", "current", correlation)
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.scoped(current).readChangeIndex("/w")
	if err != nil {
		t.Fatal(err)
	}
	change := index.Changes[id]
	if next != "apple1" || len(index.Changes) != 2 || index.Changes[next].Correlation != correlation {
		t.Fatalf("new reservation missing after cleanup: next=%q index=%+v", next, index)
	}
	if change.RetiredCalls != 1 || len(change.Calls) != 1 || change.Calls[0].ID != "current-call" {
		t.Fatalf("persisted attempts = %+v", change)
	}
	if index.Streams[0].Next != 1 || index.Streams[0].Retired != 0 {
		t.Fatalf("partially retained stream = %+v", index.Streams[0])
	}
	retentionTestExists(t, store, "/w", "old-call", false)
	retentionTestExists(t, store, "/w", "current-call", true)
}

func TestStorageLegacyJournalPressurePreservesReceipts(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journals := newJournalStore()
	if err := journals.initialize(t.Context(), store, "/w", "old", "/root", ""); err != nil {
		t.Fatal(err)
	}
	retentionTestPut(t, store, t.Context(), "/w", "compacted-call")
	if _, err := journals.apply(t.Context(), store, "/w", "old", "compacted-call", []journalMutation{{Op: "add", Text: new("old milestone")}}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	for _, name := range []string{journalFilename("/w", "old"), replayRecordName("/w", "compacted-call", false)} {
		if err := os.Chtimes(filepath.Join(store.directory, name), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	resumed, _ := retentionTestSession(t, store, "old", 0)
	store.maxBytes = 1
	if err := journals.initialize(resumed, store, "/w", "old", "/root", ""); err == nil || !strings.Contains(err.Error(), "limit is 1 bytes") {
		t.Fatalf("expected capacity error, got %v", err)
	}
	retentionTestExists(t, store, "/w", "compacted-call", true)
}

func TestStorageDuplicateCursorAdoptsDependencies(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	id, err := store.putShellOutput(parent, "a\nb\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.readShellOutput(parent, id)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.putReadCursor(parent, source, [2]int{2, 0}, "")
	if err != nil {
		t.Fatal(err)
	}
	child, _ := retentionTestSession(t, store, "child", 0)
	child = bindTestHandleScope(t, store, child, "", "parent")
	source, err = store.readShellOutput(child, id)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.putReadCursor(child, source, [2]int{2, 0}, "")
	if err != nil || duplicate != cursor {
		t.Fatalf("duplicate cursor: %s %v", duplicate, err)
	}
	releaseParent()
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	if err := store.cleanupSessions(child); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readShellOutput(child, cursor); err != nil {
		t.Fatalf("published cursor lost: %v", err)
	}
}

func TestStoragePreparationProtectsLeaseFromPreviousTerminal(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &mekugiProxy{replayStore: store, activeSessions: make(map[string]int)}
	key := "/w\x00thread"
	if err := p.activateSession(key); err != nil {
		t.Fatal(err)
	}
	ctx, err := p.beginStorageSession(t.Context(), "thread", "old")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.deactivateSession(key)
		p.releaseIdleStorageSession(ctx, "thread")
	})
	// New preparation reserves activity before acquiring or sharing the turn lease.
	if err := p.activateSession(key); err != nil {
		t.Fatal(err)
	}
	ctx, err = p.beginStorageSession(t.Context(), "thread", "new")
	if err != nil {
		t.Fatal(err)
	}
	// The prior request's terminal closes at the former preparation/activation gap.
	p.deactivateSession(key)
	p.releaseIdleStorageSession(ctx, "thread")
	lease := flock.New(filepath.Join(store.directory, strings.TrimSuffix(storageSessionName("thread"), ".json")+".lock"))
	acquired, err := lease.TryLock()
	if acquired {
		_ = lease.Unlock()
	}
	if err != nil || acquired {
		t.Fatalf("running preparation lost its lease: acquired=%v err=%v", acquired, err)
	}
	p.deactivateSession(key)
	p.releaseIdleStorageSession(ctx, "thread")
	acquired, err = lease.TryLock()
	if err != nil || !acquired {
		t.Fatalf("idle completion retained its lease: acquired=%v err=%v", acquired, err)
	}
	_ = lease.Unlock()
}

func TestStorageStaleTerminalPreservesNewerHandoff(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &mekugiProxy{replayStore: store, activeSessions: make(map[string]int), commentary: newCommentaryBroker()}
	key := "/w\x00thread"
	start := func() *mekugiResponseTransform {
		t.Helper()
		if err := p.activateSession(key); err != nil {
			t.Fatal(err)
		}
		ctx, err := p.beginStorageSession(t.Context(), "thread", "routing")
		if err != nil {
			t.Fatal(err)
		}
		return &mekugiResponseTransform{ctx: ctx, proxy: p, sessionActive: true, historySessionID: key, shellThreadID: "thread"}
	}
	older := start()
	older.storageIdle = true
	newer := start()
	token := p.commentary.subscribeThread(key, "thread", "")
	newer.Close() // The host has a tool to dispatch; no terminal was delivered.
	older.Close() // Its terminal must not retire the newer request's lease or route.
	lease := flock.New(filepath.Join(store.directory, strings.TrimSuffix(storageSessionName("thread"), ".json")+".lock"))
	acquired, err := lease.TryLock()
	if acquired {
		_ = lease.Unlock()
	}
	if err != nil || acquired {
		t.Fatalf("newer handoff lost lease: acquired=%v err=%v", acquired, err)
	}
	p.commentary.mu.Lock()
	_, routeExists := p.commentary.routes[token]
	p.commentary.mu.Unlock()
	if !routeExists {
		t.Fatal("newer handoff lost publisher route")
	}
	p.releaseIdleStorageSession(newer.ctx, "thread")
}

func TestStorageRetentionLegacyHandlesAreCleanupOnly(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unowned", true: "owned"}[owned], func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const workspace = "/legacy-workspace"
			const oldID = "r_AAAAAAAAAAAAAAAAAAAAAA"
			outputName := "output-" + oldID + ".json"
			indexName := strings.Replace(changeIndexName(workspace, ""), changeIndexPrefix, "changes-", 1)
			records := map[string][]byte{
				outputName: mustMarshalJSON(shellOutputRecord{Version: 1, ID: oldID, Stdout: "old output"}),
				indexName: mustMarshalJSON(changeIndex{
					Version: 1, Workspace: workspace,
					Streams: []changeStream{{Thread: "old", Next: 1}},
					Changes: map[string]trackedChange{"hp_a1": {Correlation: "old-call"}},
				}),
			}
			for name, data := range records {
				if err := os.WriteFile(filepath.Join(store.directory, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if owned {
				catalog := retainedSession{Version: 1, Thread: "old", LastUsed: time.Now(),
					Files: map[string]bool{outputName: true, indexName: true}}
				if err := os.WriteFile(filepath.Join(store.directory, storageSessionName("old")), mustMarshalJSON(catalog), 0600); err != nil {
					t.Fatal(err)
				}
				// Resuming the original owner must not fail on old output names.
				_, release := retentionTestSession(t, store, "old", 0)
				release()
			}
			current, _ := retentionTestSession(t, store, "current", 0)
			if err := store.retainInput(current, workspace, nil, nil, nil); err != nil {
				t.Fatalf("new request blocked by old index: %v", err)
			}
			id, err := store.reserveChange(current, workspace, "current", "new-call")
			if err != nil || id != "amber1" {
				t.Fatalf("new index allocation = %q, %v", id, err)
			}
			if _, err := store.putShellOutput(current, "new output", "", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := store.readShellOutput(current, oldID); err == nil {
				t.Fatal("legacy read reference became usable")
			}
			if err := store.cleanupSessions(current); err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.storageSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range records {
				if snapshot.files[name] != int64(len(data)) || snapshot.owners[name] != 1 {
					t.Fatalf("old storage lost accounting or ownership: %s", name)
				}
				got, err := os.ReadFile(filepath.Join(store.directory, name))
				if err != nil || string(got) != string(data) {
					t.Fatalf("old storage was modified before expiration: %s, %v", name, err)
				}
			}
			if owned {
				retentionTestAge(t, store, "old", 15*24*time.Hour)
			} else {
				expired := time.Now().Add(-15 * 24 * time.Hour)
				for name := range records {
					if err := os.Chtimes(filepath.Join(store.directory, name), expired, expired); err != nil {
						t.Fatal(err)
					}
				}
			}
			sweepTime := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(filepath.Join(store.directory, "retention-sweep"), sweepTime, sweepTime); err != nil {
				t.Fatal(err)
			}
			if err := store.cleanupSessions(current); err != nil {
				t.Fatalf("old storage blocked cleanup: %v", err)
			}
			for name := range records {
				if _, err := os.Stat(filepath.Join(store.directory, name)); !os.IsNotExist(err) {
					t.Fatalf("expired legacy record survived: %s, %v", name, err)
				}
			}
			index, err := store.scoped(current).readChangeIndex(workspace)
			if err != nil || index.Changes[id].Correlation != "new-call" {
				t.Fatalf("legacy cleanup damaged current index: %+v, %v", index, err)
			}
		})
	}
}

func BenchmarkStorageReserveChange(b *testing.B) {
	store, err := openMekugiReplayStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	ctx, release, err := store.beginSession(b.Context(), "current", "current")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(release)
	const existing = 128
	index := changeIndex{
		Version: 1, Workspace: "/w",
		Streams: []changeStream{{Thread: "current", Next: existing}},
		Changes: make(map[string]trackedChange, existing),
	}
	for number := 1; number <= existing; number++ {
		index.Changes[changeHandle("a", number)] = trackedChange{
			Correlation: strings.Repeat("x", 2048) + strconv.Itoa(number),
		}
	}
	baseline, err := marshalProtocolJSON(index)
	if err != nil {
		b.Fatal(err)
	}
	name := changeIndexName(index.Workspace, index.Namespace)
	if err := store.locked(ctx, func() error { return store.scoped(ctx).retainFiles(name) }); err != nil {
		b.Fatal(err)
	}
	// Restore the same durable input outside the measurement: each iteration
	// reserves one new ID against an equally sized, session-owned index.
	for b.Loop() {
		b.StopTimer()
		if err := store.locked(ctx, func() error {
			return store.writeFile(name, "changes-pending-", baseline)
		}); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		id, err := store.reserveChange(ctx, index.Workspace, "current", "next")
		if err != nil || id != changeHandle("a", existing+1) {
			b.Fatalf("reserve change = %q, %v", id, err)
		}
	}
	persisted, err := store.readChangeIndex(index.Workspace)
	if err != nil || len(persisted.Changes) != existing+1 || persisted.Changes["amber129"].Correlation != "next" {
		b.Fatalf("persisted reservation: changes=%d err=%v", len(persisted.Changes), err)
	}
}
