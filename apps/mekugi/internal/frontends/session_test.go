package frontends

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type deliveryWriter func([]byte) (int, error)

func (write deliveryWriter) Write(p []byte) (int, error) { return write(p) }

func TestFrontendSessionLeaseCoversDeliveryAndWriteFailure(t *testing.T) {
	registry := sharedTestRegistry(t)
	for _, failure := range []string{"", "stdout", "stderr"} {
		t.Run("failure="+failure, func(t *testing.T) {
			thread := uniqueTestThread("delivery")
			t.Setenv("CODEX_THREAD_ID", thread)
			service := testHistory()
			store := service.Attach(registry.ReplayDirectory)
			leaseName := strings.TrimSuffix(history.SessionName(thread), ".json") + ".lock"
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte("delivered\n"), 0600); err != nil {
				t.Fatal(err)
			}
			writeError := errors.New("delivery stopped")
			var stdout, stderr bytes.Buffer
			writes := 0
			writer := func(stream string, buffer *bytes.Buffer) deliveryWriter {
				return func(p []byte) (int, error) {
					writes++
					acquired, release, err := store.TryPruneLease(leaseName)
					if release != nil {
						release()
					}
					if err != nil || acquired {
						t.Errorf("%s delivery lease: prune acquired=%t error=%v", stream, acquired, err)
					}
					catalog, err := store.ReadCatalog(history.SessionName(thread))
					if err != nil || catalog.Thread != thread {
						t.Errorf("catalog before %s delivery: thread=%q error=%v", stream, catalog.Thread, err)
					}
					if stream == failure {
						return 0, writeError
					}
					return buffer.Write(p)
				}
			}
			handled, status := runToolPluginWorkerInHostProcess(service, testRecovery(), testCommands(), t.Context(), registry.Frontends["mcat"], []string{path}, nil, writer("stdout", &stdout), writer("stderr", &stderr))
			if !handled || writes == 0 {
				t.Fatalf("handled=%t delivery writes=%d", handled, writes)
			}
			if failure == "" {
				if status != 0 || stdout.String() != "delivered\n" || stderr.Len() != 0 {
					t.Errorf("status=%d stdout=%q stderr=%q", status, &stdout, &stderr)
				}
			} else if status == 0 || failure == "stdout" && !strings.Contains(stderr.String(), writeError.Error()) {
				t.Errorf("write failure status=%d stderr=%q", status, &stderr)
			}
			acquired, release, err := store.TryPruneLease(leaseName)
			if release != nil {
				defer release()
			}
			if err != nil || !acquired {
				t.Fatalf("lease after delivery: prune acquired=%t error=%v", acquired, err)
			}
		})
	}
}

func TestFrontendRejectsMissingOrInvalidStoreWithoutCreatingStorage(t *testing.T) {
	for _, state := range []string{"missing-directory", "missing-lock", "invalid-lock"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			replay := filepath.Join(directory, "replay")
			if state != "missing-directory" {
				if err := os.Mkdir(replay, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if state == "invalid-lock" {
				if err := os.Mkdir(filepath.Join(replay, "store.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			registry, err := New(t.Context(), Config{Commands: testCommands(), RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: replay})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			t.Setenv("CODEX_THREAD_ID", uniqueTestThread("invalid-store"))
			var stdout, stderr bytes.Buffer
			handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), registry.Frontends["mcat"], []string{filepath.Join(directory, "not-read")}, nil, &stdout, &stderr)
			if !handled || status == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "storage is missing or invalid") {
				t.Fatalf("handled=%t status=%d stdout=%q stderr=%q", handled, status, &stdout, &stderr)
			}
			entries, err := os.ReadDir(replay)
			if state == "missing-directory" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing store was created: entries=%v error=%v", entries, err)
				}
			} else {
				wantEntries := 0
				if state == "invalid-lock" {
					wantEntries = 1
				}
				if err != nil || len(entries) != wantEntries {
					t.Fatalf("invalid store was modified: entries=%v error=%v", entries, err)
				}
			}
		})
	}
}
