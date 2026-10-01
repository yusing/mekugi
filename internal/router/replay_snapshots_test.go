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
	"sync"
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
	if err := store.put(ctx, "/w", map[string]mekugiHistory{call: h}); err != nil {
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

func TestReplaySnapshotsStorageFailureDoesNotPublishCall(t *testing.T) {
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

// A record's blobs share one admission: pressure rejects the whole record
// before any blob is written, instead of leaving earlier blobs behind.
func TestReplaySnapshotsAdmitRecordBlobsTogether(t *testing.T) {
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
	snapshotTestPut(t, store, t.Context(), "call", mekugiHistory{ToolName: "exec", ExecObservation: &execObservation{Files: files}})
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 5 {
		t.Fatalf("blobs = %v, want four texts and one manifest", blobs)
	}
	got, found, err := store.lookup(t.Context(), "/w", "call")
	if err != nil || !found || !reflect.DeepEqual(got.ExecObservation.Files, files) {
		t.Fatalf("round trip found=%v err=%v", found, err)
	}
}

func TestReplaySnapshotsForkDependenciesSurviveCleanup(t *testing.T) {
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

func TestReplaySnapshotsConcurrentStoresShareObjects(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			other, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Error(err)
				return
			}
			if err := other.put(t.Context(), "/w", map[string]mekugiHistory{fmt.Sprint(i): snapshotTestHistory()}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 3 {
		t.Fatalf("concurrent calls duplicated snapshots: %v", blobs)
	}
}

func TestReplaySnapshotsPressureProtectsUnpublishedDependencies(t *testing.T) {
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

// Quantify actual persisted files from real bounded workspace captures. The
// fixture models repeated baselines but deliberately uses high-entropy UTF-8,
// so the regression cannot pass through highly compressible prose alone.
func TestReplaySnapshotsQuantitativeStorageRegression(t *testing.T) {
	workspace, legacyDir := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	content := make([]byte, 128<<10)
	for i := range content {
		content[i] = byte(33 + rng.IntN(90))
	}
	for i := range 4 {
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("file-%d.txt", i)), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var observation *execObservation
	manifests, contents := make(map[string]bool), make(map[string]bool)
	for i := range 48 {
		if i < 44 && i%2 == 0 {
			content[0] = byte('A' + i/2)
			if err := os.WriteFile(filepath.Join(workspace, "file-0.txt"), content, 0600); err != nil {
				t.Fatal(err)
			}
			observation = observeTestCommand(t, workspace, "printf a > file-0.txt; printf a > file-1.txt; printf a > file-2.txt; printf a > file-3.txt")
			if len(observation.Files) != 4 || len(observation.Omitted) != 0 {
				t.Fatalf("workload capture incomplete: files=%d omitted=%v", len(observation.Files), observation.Omitted)
			}
		}
		call := fmt.Sprintf("call-%02d", i)
		h := mekugiHistory{ToolName: "exec", Script: call, ExecObservation: observation}
		data, err := marshalProtocolJSON(replayRecord{Version: 1, Workspace: "/w", CallID: call, History: h})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacyDir, call+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		snapshotTestPut(t, store, t.Context(), call, h)
		stored, err := os.ReadFile(filepath.Join(store.directory, replayRecordName("/w", call, false)))
		if err != nil {
			t.Fatal(err)
		}
		var envelope replayRecord
		if err := json.Unmarshal(stored, &envelope); err != nil || envelope.Snapshots == nil {
			t.Fatalf("missing compact envelope: %v", err)
		}
		manifests[envelope.Snapshots.ExecFiles] = true
		for _, name := range envelope.Snapshots.Contents {
			contents[name] = true
		}
		got, found, err := store.lookup(t.Context(), "/w", call)
		if err != nil || !found || !reflect.DeepEqual(got.ExecObservation, durableHistory(h).ExecObservation) {
			t.Fatalf("persisted capture differs for %s: %v %v", call, found, err)
		}
	}
	measure := func(dir string) int64 {
		var total int64
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			total += info.Size()
		}
		return total
	}
	legacy, compact := measure(legacyDir), measure(store.directory)
	blobs := len(snapshotTestBlobs(t, store))
	t.Logf("persisted bytes: inline=%d compact=%d reduction=%.2f%%; calls=48 manifests=%d content objects=%d", legacy, compact, 100*(1-float64(compact)/float64(legacy)), len(manifests), len(contents))
	if len(manifests) != 22 || len(contents) != 23 || blobs != 45 || compact*100 >= legacy*20 {
		t.Fatalf("storage regression: inline=%d compact=%d manifests=%d contents=%d blobs=%d", legacy, compact, len(manifests), len(contents), blobs)
	}
}

func TestReplaySnapshotsLegacyCatalogProtectsDependencies(t *testing.T) {
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
