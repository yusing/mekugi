package capturer

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/persistence"
)

func TestStorageWriteSnapshotSharedAndDetached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	counter := new(persistence.Counter)
	recorder, err := New(Config{Output: path, Mode: "mekugi", Writes: counter})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(`{"model":"test","input":[]}`)))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	first := recorder.Snapshot()
	if first.StorageWrites == nil || info.Size() == 0 || first.StorageWrites.Bytes != uint64(info.Size()) {
		t.Fatalf("snapshot=%+v size=%d", first.StorageWrites, info.Size())
	}
	if err := persistence.AtomicFile(filepath.Join(t.TempDir(), "record"), "pending-*", []byte("managed"), counter); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := recorder.WriteMetrics(&encoded); err != nil {
		t.Fatal(err)
	}
	var exported MetricsSnapshot
	if err := json.Unmarshal(encoded.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.StorageWrites.Bytes != uint64(info.Size())+7 || first.StorageWrites.Bytes != uint64(info.Size()) {
		t.Fatalf("counter was not shared/detached: %v / %v", first.StorageWrites, exported.StorageWrites)
	}
	plain, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if plain.Snapshot().StorageWrites != nil {
		t.Fatal("disabled counter reported zero")
	}
}
