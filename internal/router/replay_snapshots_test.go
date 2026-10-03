package router

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func snapshotTestHistory() mekugiHistory {
	files := []execFileSnapshot{
		{Path: "/w/text", Kind: execFileText, Content: "before\r\n<&>\n", Size: 14},
		{Path: "/w/binary", Kind: execFileBinary, Size: 17, Hash: "binary-hash"},
		{Path: "/w/link", Kind: execFileSymlink, Link: "../target"},
		{Path: "/w/missing", Kind: execFileAbsent},
		{Path: "/w/incomplete", Error: "bounded capture"},
	}
	return mekugiHistory{
		ToolName: "exec", Script: "original input", CarrierPayload: "unchanged stock input",
		ResolvedBaseline: &resolvedStockBaseline{Root: "/w", Shell: "bash", Files: files,
			Omitted: []execOmission{{Path: "/w/omitted", Reason: "time bound"}}},
		ExecObservation: &execObservation{Files: files, Class: "scoped", WindowStart: time.Unix(1700000000, 0).UTC(),
			Listings: []execListing{{Root: "/w", Entries: map[string]string{"text": "f"}}}},
		NativePatches: []nativePatchObservation{{Input: "exact patch input", Files: []nativePatchFileSnapshot{{
			BeforePath: "/w/text", AfterPath: "/w/renamed", Before: "old\n", Exists: true,
			TargetBefore: "target\n", TargetExists: true, TargetError: "target diagnostic",
		}}}, {Input: "no files"}},
		ReviewFiles: []mekugi.ReviewFile{{BeforePath: "/w/text", AfterPath: "/w/text", Diff: "-old\n+new\n"},
			{BeforePath: "/w/incomplete", Incomplete: "missing before"}},
		HostResults: []nativeToolResult{{Tool: applyPatchToolName}},
	}
}

func snapshotTestPut(t *testing.T, store *mekugiReplayStore, ctx context.Context, call string, h mekugiHistory) {
	t.Helper()
	if err := putLegacySnapshotFixture(store, ctx, call, h); err != nil {
		t.Fatal(err)
	}
}

func snapshotTestBlobs(t *testing.T, store *mekugiReplayStore) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(store.directory, "snapshot-*.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestReplaySnapshotsRoundTripDedupAndLegacy(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := snapshotTestHistory()
	original := durableHistory(h)
	for _, call := range []string{"first", "second", "first"} {
		snapshotTestPut(t, store, t.Context(), call, h)
	}
	if !reflect.DeepEqual(h, original) {
		t.Fatal("storage changed the captured evidence in memory")
	}
	// Exec and workspace inventories share the same content-addressed collection.
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 3 {
		t.Fatalf("duplicate evidence objects: %v", blobs)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"first", "second"} {
		got, found, err := reopened.lookup(t.Context(), "/w", call)
		if err != nil || !found || !reflect.DeepEqual(got, original) {
			t.Fatalf("restored evidence differs: found=%v err=%v got=%+v", found, err, got)
		}
	}
	data, err := marshalProtocolJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "legacy", History: original})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeFile(replayRecordName("/w", "legacy", false), "legacy-pending-", data); err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.lookup(t.Context(), "/w", "legacy")
	if err != nil || !found || !reflect.DeepEqual(got, original) {
		t.Fatalf("legacy evidence changed: %v %v", found, err)
	}
	h.ExecObservation.Files[0].Content = "conflicting content"
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"first": h}); err == nil {
		t.Fatal("accepted changed durable evidence")
	}
}

func TestReplaySnapshotsRejectUnavailableAndCorruptEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "hash mismatch", "gzip corrupt", "symlink", "expansion bound"} {
		t.Run(kind, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			snapshotTestPut(t, store, t.Context(), "call", snapshotTestHistory())
			path := snapshotTestBlobs(t, store)[0]
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "hash mismatch", "expansion bound":
				var data bytes.Buffer
				z := gzip.NewWriter(&data)
				payload := "[]"
				if kind == "expansion bound" {
					payload = strings.Repeat("x", maxReplayRecordBytes+1)
				}
				if _, err := z.Write([]byte(payload)); err != nil {
					t.Fatal(err)
				}
				if err := z.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			case "gzip corrupt":
				if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), path); err != nil {
					t.Fatal(err)
				}
			}
			if _, found, err := store.lookup(t.Context(), "/w", "call"); err == nil || found {
				t.Fatalf("unavailable evidence claimed readable: found=%v err=%v", found, err)
			}
			if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"call": snapshotTestHistory()}); err == nil {
				t.Fatal("retry claimed durable success with invalid dependencies")
			}
		})
	}
}

func TestInlineEditStorageFailureDoesNotPublishCall(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes = 1
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"call": snapshotTestHistory()}); err == nil {
		t.Fatal("accepted storage overflow")
	}
	if _, found, err := store.lookup(t.Context(), "/w", "call"); err != nil || found {
		t.Fatalf("published an envelope before snapshots were durable: %v %v", found, err)
	}
}

// Inline evidence and its envelope are admitted atomically, without snapshot files.
func TestInlineEditEvidenceAdmissionIsAtomic(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(1, 2))
	var files []execFileSnapshot
	for i := range 4 {
		content := make([]byte, 4096)
		for j := range content {
			content[j] = byte('a' + random.IntN(26))
		}
		files = append(files, execFileSnapshot{Path: fmt.Sprintf("/w/%d", i), Kind: execFileText, Content: string(content), Size: int64(len(content))})
	}
	store.maxBytes = 10 << 10
	err = store.put(t.Context(), "/w", map[string]mekugiHistory{"call": {ToolName: "exec", ExecObservation: &execObservation{Files: files}}})
	if err == nil {
		t.Fatal("accepted a record whose blobs exceed the quota")
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 0 {
		t.Fatalf("rejected record left blobs behind: %v", blobs)
	}
	store.maxBytes = 0
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"call": {ToolName: "exec", ExecObservation: &execObservation{Files: files}}}); err != nil {
		t.Fatal(err)
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 0 {
		t.Fatalf("inline record produced snapshot blobs: %v", blobs)
	}
	got, found, err := store.lookup(t.Context(), "/w", "call")
	if err != nil || !found || !reflect.DeepEqual(got.ExecObservation.Files, files) {
		t.Fatalf("round trip found=%v err=%v", found, err)
	}
}

func TestReplaySnapshotsForkDependenciesSurviveCleanup(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, release := retentionTestSession(t, store, "parent", 0)
	h := snapshotTestHistory()
	snapshotTestPut(t, store, parent, "shared", h)
	child, releaseChild := retentionTestSession(t, store, "fork", 0)
	if err := store.retainInput(child, "/w", nil, map[string]mekugiHistory{"shared": h}, nil); err != nil {
		t.Fatal(err)
	}
	release()
	retentionTestAge(t, store, "parent", 15*24*time.Hour)
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.lookup(t.Context(), "/w", "shared")
	if err != nil || !found || !reflect.DeepEqual(got, durableHistory(h)) {
		t.Fatalf("fork lost shared snapshots after parent expiry/restart: %v %v", found, err)
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 3 {
		t.Fatalf("fork dependencies reclaimed: %v", blobs)
	}
	releaseChild()
	retentionTestAge(t, store, "fork", 15*24*time.Hour)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.directory, "retention-sweep"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 0 {
		t.Fatalf("last owner left snapshot artifacts: %v", blobs)
	}
}

func TestReplaySnapshotsRetainedChangeOutputAfterRestart(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, release := retentionTestSession(t, store, "parent", 0)
	id, err := store.reserveChange(parent, "/w", "parent", "edit")
	if err != nil {
		t.Fatal(err)
	}
	h := snapshotTestHistory()
	h.ChangeID, h.CorrelationID, h.Attempt = id, "edit", 1
	snapshotTestPut(t, store, parent, "edit-call", h)
	text, snapshot, err := store.readChangeView(parent, changeReadOptions{workspace: "/w", ids: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "-old\n+new") {
		t.Fatalf("missing actual persisted edit: %q", text)
	}
	reference, err := store.putChangeRead(parent, snapshot, text, 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	child, _ := retentionTestSession(t, store, "reader", 0)
	child = bindTestHandleScope(t, store, child, "", "parent")
	if _, err := store.readShellOutput(child, reference); err != nil {
		t.Fatal(err)
	}
	retentionTestAge(t, store, "parent", 15*24*time.Hour)
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	record, err := reopened.readShellOutput(child, reference)
	if err != nil {
		t.Fatal(err)
	}
	output, err := reopened.readSourceStreams(child, record)
	if err != nil || output.Stdout != text[1:] {
		t.Fatalf("retained change output differs after expiry/restart: %q err=%v", output.Stdout, err)
	}
}

func TestReplaySnapshotsPressureProtectsUnpublishedDependencies(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := snapshotTestHistory()
	h.UpstreamItem = map[string]json.RawMessage{"status": json.RawMessage(`"in_progress"`)}
	snapshotTestPut(t, store, t.Context(), "call", h)
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	old := time.Now().Add(-15 * 24 * time.Hour)
	for name, bytes := range files {
		size += bytes
		if err := os.Chtimes(filepath.Join(store.directory, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	store.maxBytes = size / 2
	h.UpstreamItem["status"] = json.RawMessage(`"completed"`)
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"call": h}); err == nil {
		t.Fatal("quota check did not protect pending dependencies")
	}
	got, found, err := store.lookup(t.Context(), "/w", "call")
	if err != nil || !found || string(got.UpstreamItem["status"]) != `"in_progress"` {
		t.Fatalf("failed update damaged retained facts: found=%v err=%v", found, err)
	}
}

func TestReplaySnapshotsLegacyCatalogProtectsDependencies(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, store, "owner", 0)
	snapshotTestPut(t, store, ctx, "call", snapshotTestHistory())
	// Simulate a catalog containing only the envelope, then let cleanup expand
	// dependencies without decompressing them or borrowing another session.
	if err := store.locked(t.Context(), func() error {
		catalog, err := store.readRetainedSession(storageSessionName("owner"))
		if err != nil {
			return err
		}
		catalog.Files = map[string]bool{replayRecordName("/w", "call", false): true}
		data, err := json.Marshal(catalog)
		if err != nil {
			return err
		}
		return store.writeFile(storageSessionName("owner"), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range snapshotTestBlobs(t, store) {
		old := time.Now().Add(-15 * 24 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.lookup(t.Context(), "/w", "call"); err != nil || !found {
		t.Fatalf("live call dependency deleted: %v %v", found, err)
	}
	release()
}
