package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestResponseTranslationRetriesPersistenceAfterBackgroundCleanup(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				attachTestReplayStore(t, proxy)
				store := proxy.replayStore
				workspace := t.TempDir()
				old, releaseOld := retentionTestSession(t, store, "old", 0)
				if err := store.put(old, workspace, map[string]mekugiHistory{
					"old-call": {ToolName: "shell", Script: strings.Repeat("old evidence ", 1000)},
					"shared":   {ToolName: "shell", Script: "shared evidence"},
				}); err != nil {
					t.Fatal(err)
				}
				releaseOld()
				retentionTestAge(t, store, "old", time.Hour)
				current, _ := retentionTestSession(t, store, "thread-1", 0)
				shared, found, err := store.lookup(current, workspace, "shared")
				if err != nil || !found {
					t.Fatalf("shared record: found=%v err=%v", found, err)
				}
				if err := store.retainInput(current, workspace, nil, map[string]mekugiHistory{"shared": shared}, nil); err != nil {
					t.Fatal(err)
				}
				running, _ := retentionTestSession(t, store, "running", 0)
				retentionTestPut(t, store, running, workspace, "running-call")
				const arguments = `{"cmd":"printf evidence > authored.txt","shell":"bash"}`
				item := map[string]any{"type": "function_call", "id": "item", "call_id": "new-call",
					"name": nativeExecCommandToolName, "arguments": arguments, "status": "completed"}
				response := map[string]any{"id": "response", "status": "completed", "output": []any{item}}
				upstream := serverHTTPResponse(string(mustTestJSON(t, response)))
				if stream {
					body := "data: " + string(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})) + "\n\n" +
						"data: " + string(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response})) + "\n\n"
					upstream.Header = http.Header{"Content-Type": []string{"text/event-stream"}}
					upstream.Body = io.NopCloser(strings.NewReader(body))
				}
				provider := &responseStoragePressureProvider{
					serverFakeProvider: serverFakeProvider{results: []serverForwardResult{{response: upstream}}},
					fill: func() {
						// Fill the store after request preparation, while the upstream
						// response is arriving, to exercise response translation itself.
						files, err := store.storageFileSizes()
						if err != nil {
							t.Fatal(err)
						}
						store.maxBytes, _ = store.storageNeeds(files, "", 0)
						startResponseRetentionWorker(t, store)
					},
				}
				request := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = stream
					fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
					fields["tools"] = testNativeResponsesTools()
				})
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var visible bytes.Buffer
				if err := executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &visible, nil, proxy); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 || !bytes.Contains(visible.Bytes(), []byte("new-call")) {
					t.Fatalf("response not delivered exactly once: forwards=%d visible=%s", len(provider.forwarded), visible.Bytes())
				}
				retentionTestExists(t, store, workspace, "old-call", false)
				retentionTestExists(t, store, workspace, "shared", true)
				retentionTestExists(t, store, workspace, "running-call", true)
				reopened, err := openMekugiReplayStore(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				history, found, err := reopened.lookup(t.Context(), workspace, "new-call")
				if err != nil || !found || jsonString(history.UpstreamItem, "arguments") != arguments {
					t.Fatalf("completed evidence not durable: found=%v err=%v history=%+v", found, err, history)
				}
			})
		})
	}
}

type responseStoragePressureProvider struct {
	serverFakeProvider
	fill func()
}

func (p *responseStoragePressureProvider) forwardExecution(ctx, requestCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	p.fill()
	return p.serverFakeProvider.forwardExecution(ctx, requestCtx, body, headers, cacheKey)
}

func startResponseRetentionWorker(t *testing.T, store *mekugiReplayStore) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); store.runRetentionSweeps(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestReplayStorageDefaultIsFourGiB(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker, err := shellOutputStore(toolWorkerManifest{ReplayDirectory: store.directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*mekugiReplayStore{store, worker, {}} {
		// The reported failure needed only 170 bytes over the old 1 GiB cap.
		// Admission checks sizes, so no large payload allocation is needed.
		_, limit := candidate.storageNeeds(nil, "", (1<<30)+170)
		if limit != 4<<30 {
			t.Fatalf("storage limit = %d, want 4 GiB", limit)
		}
	}
	for number := range 33 {
		name := replayRecordName("/w", "existing-"+strconv.Itoa(number), false)
		file, err := os.Create(filepath.Join(store.directory, name))
		if err != nil {
			t.Fatal(err)
		}
		size := int64(maxReplayRecordBytes)
		if number == 32 {
			size = 170
		}
		if err := file.Truncate(size); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.locked(t.Context(), func() error { return store.maintainStorage("", 1) }); err != nil {
		t.Fatalf("admission still rejects storage above the old cap: %v", err)
	}
}

func TestResponsePersistencePressureStopsWithoutReclaimableData(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := retentionTestSession(t, store, "active", 0)
	retentionTestPut(t, store, current, "/w", "existing")
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	startResponseRetentionWorker(t, store)
	ctx, cancel := context.WithTimeout(current, 3*time.Second)
	defer cancel()
	err = store.putAfterMaintenance(ctx, "/w", map[string]mekugiHistory{"new": {ToolName: "shell", Script: "true"}})
	if diagnostic, ok := errors.AsType[*criticalDiagnosticError](err); !ok || diagnostic.code != "storage_capacity" || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("protected storage must fail without endlessly retrying: %v", err)
	}
	retentionTestExists(t, store, "/w", "existing", true)
	retentionTestExists(t, store, "/w", "new", false)
}

func TestResponsePersistenceMaintenanceWaitIsCancellable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		current, _ := retentionTestSession(t, store, "active", 0)
		retentionTestPut(t, store, current, "/w", "existing")
		files, err := store.storageFileSizes()
		if err != nil {
			t.Fatal(err)
		}
		store.maxBytes, _ = store.storageNeeds(files, "", 0)
		ctx, cancel := context.WithTimeout(current, 100*time.Millisecond)
		defer cancel()
		err = store.putAfterMaintenance(ctx, "/w", map[string]mekugiHistory{"new": {ToolName: "shell", Script: "true"}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("maintenance wait ignored cancellation: %v", err)
		}
		if diagnostic, ok := errors.AsType[*criticalDiagnosticError](err); !ok || diagnostic.code != "storage_capacity" {
			t.Fatalf("wait lost original capacity diagnostic: %v", err)
		}
		retentionTestExists(t, store, "/w", "existing", true)
		retentionTestExists(t, store, "/w", "new", false)
	})
}

func TestResponsePersistenceRetriesSuccessiveHistoryWrites(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range []string{"old1", "old2", "old3"} {
			ctx, release := retentionTestSession(t, store, thread, 0)
			if err := store.put(ctx, "/w", map[string]mekugiHistory{
				thread: {ToolName: "shell", Script: strings.Repeat("old", 167)},
			}); err != nil {
				t.Fatal(err)
			}
			release()
			retentionTestAge(t, store, thread, time.Hour)
		}
		current, _ := retentionTestSession(t, store, "active", 0)
		files, err := store.storageFileSizes()
		if err != nil {
			t.Fatal(err)
		}
		used, _ := store.storageNeeds(files, "", 0)
		store.maxBytes = used + 900
		startResponseRetentionWorker(t, store)
		ctx, cancel := context.WithTimeout(current, 5*time.Second)
		defer cancel()
		histories := map[string]mekugiHistory{
			"a": {ToolName: "shell", Script: strings.Repeat("a", 1000)},
			"b": {ToolName: "shell", Script: strings.Repeat("b", 1000)},
		}
		// Saving a succeeds after the first reclamation round, but fills enough
		// space to require another round for b. Partial publication is progress.
		if err := store.putAfterMaintenance(ctx, "/w", histories); err != nil {
			t.Fatal(err)
		}
		for id, want := range histories {
			got, found, err := store.lookup(ctx, "/w", id)
			if err != nil || !found || got.Script != want.Script {
				t.Fatalf("history %s not retained: found=%v err=%v history=%+v", id, found, err, got)
			}
		}
	})
}
