package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestResponseTranslationRetriesPersistenceAfterBackgroundCleanup(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				attachTestReplayStore(t, proxy)
				store := testProxyStore(proxy)
				workspace := t.TempDir()
				old, releaseOld := retentionTestSession(t, store, "old", 0)
				if err := store.put(old, workspace, map[string]legacycall.History{
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
				if err := store.retainInput(current, workspace, nil, map[string]legacycall.History{"shared": shared}, nil); err != nil {
					t.Fatal(err)
				}
				running, _ := retentionTestSession(t, store, "running", 0)
				retentionTestPut(t, store, running, workspace, "running-call")
				const source = `text(await tools.exec_command({cmd:"printf evidence > authored.txt",shell:"bash"}))`
				item := map[string]any{"type": "custom_tool_call", "id": "item", "call_id": "new-call",
					"name": "exec", "input": source, "status": "completed"}
				response := map[string]any{"id": "response", "status": "completed", "output": []any{item}}
				upstream := serverHTTPResponse(string(mustTestJSON(t, response)))
				if stream {
					body := "data: " + string(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})) + "\n\n" +
						"data: " + string(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response})) + "\n\n"
					upstream.Header = http.Header{"Content-Type": []string{"text/event-stream"}}
					upstream.Body = io.NopCloser(strings.NewReader(body))
				}
				files, err := store.testFileSizes()
				if err != nil {
					t.Fatal(err)
				}
				used, _ := store.publication.Usage(store.retentionLimits(), files, "", 0)
				store.maxBytes = used + (64 << 10)
				setTestProxyStore(proxy, store)
				provider := &responseStoragePressureProvider{
					serverFakeProvider: serverFakeProvider{results: []serverForwardResult{{response: upstream}}},
					fill: func() {
						// Fill the store after request preparation, while the upstream
						// response is arriving, to exercise response translation itself.
						files, err := store.testFileSizes()
						if err != nil {
							t.Fatal(err)
						}
						used, _ := store.publication.Usage(store.retentionLimits(), files, "", 0)
						// Grow an already-managed inactive record with valid JSON
						// whitespace. Limits are fixed before request preparation.
						name := legacycall.Name(workspace, "old-call", false)
						data, err := os.ReadFile(filepath.Join(store.directory, name))
						if err != nil {
							t.Fatal(err)
						}
						data = append(data, strings.Repeat(" ", int(store.maxBytes-used))...)
						if err := store.publication.Transaction(t.Context(), store.writes, func(tx *history.Transaction) error {
							return tx.Write(name, "pressure-", data)
						}); err != nil {
							t.Fatal(err)
						}
						startResponseRetentionWorker(t, store)
					},
				}
				request := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = stream
					fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
					fields["tools"] = testExecResponsesTools()
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
				if err != nil || !found || responses.StringField(history.UpstreamItem, "input") != source {
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

func (p *responseStoragePressureProvider) ForwardExecution(ctx, requestCtx context.Context, body []byte, headers http.Header, cacheKey string) (*inferencegateway.Response, error) {
	p.fill()
	return p.serverFakeProvider.ForwardExecution(ctx, requestCtx, body, headers, cacheKey)
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
	for _, candidate := range []*mekugiReplayStore{store, testReplayStore(mekugiReplayStore{})} {
		// The reported failure needed only 170 bytes over the old 1 GiB cap.
		// Admission checks sizes, so no large payload allocation is needed.
		_, limit := candidate.publication.Usage(candidate.retentionLimits(), nil, "", (1<<30)+170)
		if limit != 4<<30 {
			t.Fatalf("storage limit = %d, want 4 GiB", limit)
		}
	}
	for number := range 33 {
		name := legacycall.Name("/w", "existing-"+strconv.Itoa(number), false)
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
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error { return store.maintainStorage("", 1) }); err != nil {
		t.Fatalf("admission still rejects storage above the old cap: %v", err)
	}
}
