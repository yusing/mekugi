package history_test

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func openRetainedFilesStore(t *testing.T) *history.Store {
	t.Helper()
	svc, err := history.NewService([]recordkind.Spec{
		history.FrozenFileRecord,
		history.FrozenResourceRecord,
		history.LegacyOutputRecord,
		history.LegacyCursorRecord,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := svc.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestWriteAndReadReadOnlyFile(t *testing.T) {
	store := openRetainedFilesStore(t)
	payload := []byte("frozen output line 1\nfrozen output line 2\n")
	path, name, err := store.WriteReadOnlyFile(t.Context(), "thread-1", history.Limits{}, nil, "output", ".txt", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !history.FrozenFileName(name) || !strings.HasPrefix(name, "frozen-output-") || !strings.HasSuffix(name, ".txt") {
		t.Fatalf("unexpected frozen file name: %q", name)
	}
	if filepath.Dir(path) != store.Directory() || filepath.Base(path) != name {
		t.Fatalf("unexpected frozen file path: %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o444 {
		t.Fatalf("frozen file permissions = %04o, want 0444", perm)
	}
	got, err := store.ReadReadOnlyFile(path, history.MaxRecordBytes)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("ReadReadOnlyFile = %q, %v; want %q", got, err, payload)
	}
	// Re-publishing identical content succeeds idempotently on the 0o444 target.
	path2, name2, err := store.WriteReadOnlyFile(t.Context(), "thread-1", history.Limits{}, nil, "output", ".txt", payload)
	if err != nil || path2 != path || name2 != name {
		t.Fatalf("idempotent rewrite = %q, %q, %v", path2, name2, err)
	}
	// Invalid kind or extension is rejected.
	for _, tc := range []struct{ kind, ext string }{
		{"", ".txt"},
		{"BAD", ".txt"},
		{"-bad", ".txt"},
		{"bad-", ".txt"},
		{"output", ".bin"},
		{"../escape", ".txt"},
	} {
		if _, _, err := store.WriteReadOnlyFile(t.Context(), "thread-1", history.Limits{}, nil, tc.kind, tc.ext, payload); err == nil {
			t.Errorf("accepted invalid frozen spec kind=%q ext=%q", tc.kind, tc.ext)
		}
	}
	// Containment checks reject relative paths, external files, and unrecognized names.
	outside := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(outside, payload, 0o444); err != nil {
		t.Fatal(err)
	}
	unrecognized := filepath.Join(store.Directory(), "plain.txt")
	if err := os.WriteFile(unrecognized, payload, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, badPath := range []string{"", name, outside, unrecognized, filepath.Join(store.Directory(), "sub", name)} {
		if _, err := store.ReadReadOnlyFile(badPath, history.MaxRecordBytes); err == nil {
			t.Errorf("ReadReadOnlyFile accepted invalid path %q", badPath)
		}
	}
	if err := os.Remove(unrecognized); err != nil {
		t.Fatal(err)
	}
	// Retention keeps active frozen files and prunes expired thread files (even 0o444).
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retained frozen file was pruned: %v", err)
	}
	catalog, err := store.ReadCatalog(history.SessionName("thread-1"))
	if err != nil {
		t.Fatal(err)
	}
	catalog.LastUsed = time.Now().Add(-history.SessionRetention - time.Hour)
	aged, err := json.Marshal(&catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.Write(history.SessionName("thread-1"), "catalog-pending-", aged)
	}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.Directory(), "retention-sweep"), past, past); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired 0o444 frozen file survived cleanup: %v", err)
	}
}

func TestWriteAndReadJSONResource(t *testing.T) {
	store := openRetainedFilesStore(t)
	ref, err := store.WriteJSONResource(t.Context(), "thread-1", history.Limits{}, nil, map[string]string{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if !history.FrozenResourceName(ref) {
		t.Fatalf("unexpected JSON resource name: %q", ref)
	}
	data, err := store.ReadJSONResource(ref)
	if err != nil || string(data) != `{"key":"value"}` {
		t.Fatalf("ReadJSONResource = %q, %v", data, err)
	}
	for _, bad := range []string{"", "not-a-resource.json", "../" + ref, "frozen-output-" + strings.Repeat("a", 64) + ".json"} {
		if _, err := store.ReadJSONResource(bad); err == nil {
			t.Errorf("ReadJSONResource accepted invalid ref %q", bad)
		}
	}
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadJSONResource(ref); err != nil {
		t.Fatalf("retained JSON resource was pruned: %v", err)
	}
}

func TestFrozenPublicationRequiresThreadAndSweepsLegacyRecovery(t *testing.T) {
	store := openRetainedFilesStore(t)
	if _, _, err := store.WriteReadOnlyFile(t.Context(), "", history.Limits{}, nil, "output", ".txt", []byte("x")); err == nil {
		t.Fatal("WriteReadOnlyFile accepted empty thread")
	}
	if _, err := store.WriteJSONResource(t.Context(), "   ", history.Limits{}, nil, map[string]string{"a": "b"}); err == nil {
		t.Fatal("WriteJSONResource accepted blank thread")
	}

	// Simulate an upgraded store containing pre-switch outputrecovery records.
	legacyOutput := "output-" + strings.Repeat("a", 64) + ".json"
	legacyShort := "output-amber.json"
	legacyCursor := "cursor-" + strings.Repeat("b", 64) + ".json"
	for _, name := range []string{legacyOutput, legacyShort, legacyCursor} {
		if err := os.WriteFile(filepath.Join(store.Directory(), name), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Now().Add(-history.SessionRetention - time.Hour)
		if err := os.Chtimes(filepath.Join(store.Directory(), name), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	path, _, err := store.WriteReadOnlyFile(t.Context(), "expired-thread", history.Limits{}, nil, "progress", ".txt", []byte("frozen"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := store.ReadCatalog(history.SessionName("expired-thread"))
	if err != nil {
		t.Fatal(err)
	}
	catalog.LastUsed = time.Now().Add(-history.SessionRetention - time.Hour)
	aged, err := json.Marshal(&catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.Write(history.SessionName("expired-thread"), "catalog-pending-", aged)
	}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.Directory(), "retention-sweep"), past, past); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy outputrecovery records blocked expiring frozen file: %v", err)
	}
	for _, name := range []string{legacyOutput, legacyShort, legacyCursor} {
		if _, err := os.Stat(filepath.Join(store.Directory(), name)); !os.IsNotExist(err) {
			t.Fatalf("expired legacy record %s survived cleanup: %v", name, err)
		}
	}
	if !history.LegacyOutputName("output-amber.json") || !history.LegacyOutputName("output-amber2.json") {
		t.Fatal("expected retired short-handle names to match")
	}
	if history.LegacyOutputName("output-not_a_handle.json") {
		t.Fatal("unexpectedly matched non-handle output filename")
	}
}
