package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalDeliveryWorkspaceIsolation(t *testing.T) {
	for _, mode := range []string{"memory", "shared router", "independent routers"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var replay *mekugiReplayStore
				if mode != "memory" {
					var err error
					replay, err = openMekugiReplayStore(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				}
				delivery, writer := newJournalStore(), newJournalStore()
				if mode != "independent routers" {
					writer = delivery
				}
				if err := writer.initialize(t.Context(), replay, "other-workspace", "other-thread", "/root", ""); err != nil {
					t.Fatal(err)
				}
				release, err := delivery.lockDelivery(t.Context(), replay, "slow-workspace")
				if err != nil {
					t.Fatal(err)
				}
				released := make(chan struct{})
				go func() {
					time.Sleep(3 * time.Second)
					release()
					close(released)
				}()
				defer func() { <-released }()
				started := time.Now()
				ids, err := writer.apply(t.Context(), replay, "other-workspace", "other-thread", "receipt", []journalMutation{{Op: "add", Title: new("Other work")}})
				elapsed := time.Since(started)
				if err != nil || len(ids) != 1 {
					t.Fatalf("independent mutation: ids=%v error=%v", ids, err)
				}
				if elapsed >= time.Second {
					t.Fatalf("independent mutation waited %s for unrelated delivery", elapsed)
				}
				t.Logf("independent mutation wait: %s; unrelated delivery held for 3s", elapsed)
				items, err := writer.list(t.Context(), replay, "other-workspace", "other-thread")
				if err != nil || len(items) != 1 || items[0].Title != "Other work" {
					t.Fatalf("persisted independent mutation: items=%v error=%v", items, err)
				}
				<-released
				if len(delivery.deliveryGates) != 0 || len(writer.deliveryGates) != 0 {
					t.Fatal("idle delivery gates retained")
				}
			})
		})
	}
}

func TestJournalDeliveryCanceledWaiterReleasesGate(t *testing.T) {
	store := newJournalStore()
	release, err := store.lockDelivery(t.Context(), nil, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 20 {
		if unlock, err := store.lockDelivery(ctx, nil, "workspace"); err == nil {
			unlock()
			t.Fatal("canceled waiter acquired delivery")
		}
	}
	release()
	if len(store.deliveryGates) != 0 {
		t.Fatal("canceled waiters retained delivery gates")
	}
}

type delayedJournalResponse struct{ http.ResponseWriter }

func (w delayedJournalResponse) Write(data []byte) (int, error) {
	time.Sleep(500 * time.Millisecond)
	return w.ResponseWriter.Write(data)
}

func TestJournalLatencySeparatesLockAndResponseWaits(t *testing.T) {
	for _, scenario := range []struct{ phase, operation string }{
		{"delivery", "mutation"}, {"state", "mutation"}, {"replay", "mutation"},
		{"state", "read"}, {"replay", "read"}, {"state", "list"}, {"replay", "list"},
	} {
		phase, operation := scenario.phase, scenario.operation
		t.Run(phase+"/"+operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				replay, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				store := newJournalStore()
				if err := store.initialize(t.Context(), replay, "workspace", "thread", "/root", ""); err != nil {
					t.Fatal(err)
				}
				held, released := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(released)
					hold := func() error {
						close(held)
						time.Sleep(3 * time.Second)
						return nil
					}
					if phase == "replay" {
						if err := replay.locked(t.Context(), hold); err != nil {
							t.Error(err)
						}
						return
					}
					var release func()
					var err error
					if phase == "delivery" {
						release, err = store.lockDelivery(t.Context(), replay, "workspace")
					} else {
						release, err = store.lockState(t.Context())
					}
					if err != nil {
						t.Error(err)
						close(held)
						return
					}
					defer release()
					_ = hold()
				}()
				<-held
				defer func() { <-released }()
				broker := newCommentaryBroker()
				broker.debug = featureDebugOutput(t)
				broker.journalPublisher = func(ctx context.Context, _, thread, receipt string, mutations []journalMutation) ([]string, error) {
					return store.apply(ctx, replay, "workspace", thread, receipt, mutations)
				}
				broker.journalLister = func(ctx context.Context, _, thread, _ string) ([]journalItem, error) {
					return store.list(ctx, replay, "workspace", thread)
				}
				broker.journalReader = func(ctx context.Context, _, thread, agent, path string, depth *int, view string) ([]journalNode, error) {
					return store.readTree(ctx, replay, "workspace", thread, agent, path, depth, view)
				}
				token := broker.subscribe("session", "call")
				broker.bindActivity(token, "thread")
				payload := `{"id":"private-receipt","journal":{"op":"add","title":"private-title"}}`
				if operation != "mutation" {
					payload = `{"op":"` + operation + `"}`
				}
				request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, bytes.NewBufferString(payload))
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				broker.serveHTTP(delayedJournalResponse{response}, request)
				if response.Code != http.StatusOK {
					t.Fatalf("publication status: %d", response.Code)
				}
				data, err := os.ReadFile(broker.debug.log.Name())
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{token, "private-title", "private-receipt"} {
					if bytes.Contains(data, []byte(secret)) {
						t.Fatal("journal diagnostic disclosed publication content")
					}
				}
				found := false
				for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
					var event map[string]jsontext.Value
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					if string(event["event"]) != `"journal_latency"` {
						continue
					}
					found = true
					var totalWait int64
					for _, name := range []string{"delivery", "state", "replay"} {
						var wait int64
						if err := json.Unmarshal(event[name+"_wait_us"], &wait); err != nil {
							t.Fatal(err)
						}
						if name == phase && wait < 3_000_000 || name != phase && wait != 0 {
							t.Errorf("%s wait = %d microseconds for %s contention", name, wait, phase)
						}
						totalWait += wait
					}
					if string(event["response_write_us"]) != "500000" || string(event["total_us"]) != strconv.FormatInt(totalWait+500_000, 10) || string(event["persist_write_us"]) != "0" {
						t.Errorf("unexpected controlled timing: %s", line)
					}
				}
				if !found {
					t.Fatal("missing journal latency diagnostic")
				}
			})
		})
	}
}

func BenchmarkJournalConcurrentWorkspaceHTTP(b *testing.B) {
	for _, shared := range []bool{true, false} {
		name := "independent_workspace"
		if shared {
			name = "shared_workspace"
		}
		b.Run(name, func(b *testing.B) {
			replay, err := openMekugiReplayStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			delivery, writer := newJournalStore(), newJournalStore()
			if err := writer.initialize(b.Context(), replay, "workspace", "thread", "/root", ""); err != nil {
				b.Fatal(err)
			}
			if _, err := writer.apply(b.Context(), replay, "workspace", "thread", "", []journalMutation{{Op: "add", Title: new("Work")}}); err != nil {
				b.Fatal(err)
			}
			broker := newCommentaryBroker()
			broker.journalPublisher = func(ctx context.Context, _, thread, receipt string, mutations []journalMutation) ([]string, error) {
				return writer.apply(ctx, replay, "workspace", thread, receipt, mutations)
			}
			token := broker.subscribe("session", "call")
			broker.bindActivity(token, "thread")
			server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
			defer server.Close()
			sink := httpCommentarySink{endpoint: server.URL, token: token, client: commentaryHTTPClient}
			var elapsed time.Duration
			iteration := 0
			for b.Loop() {
				workspace := "workspace"
				if !shared {
					workspace = "other-workspace"
				}
				release, err := delivery.lockDelivery(b.Context(), replay, workspace)
				if err != nil {
					b.Fatal(err)
				}
				released := make(chan struct{})
				go func() {
					time.Sleep(20 * time.Millisecond)
					release()
					close(released)
				}()
				started := time.Now()
				result, err := sink.send(b.Context(), map[string]any{
					"id": strconv.Itoa(iteration), "journal": jsontext.Value(`{"op":"set","p":"/1","body":"Updated"}`),
				})
				elapsed += time.Since(started)
				<-released
				if err != nil {
					b.Fatal(err)
				}
				var response struct {
					OK bool `json:"ok"`
				}
				if err := json.Unmarshal(result, &response); err != nil || !response.OK {
					b.Fatalf("publication rejected: %s (%v)", result, err)
				}
				iteration++
			}
			b.ReportMetric(float64(elapsed.Microseconds())/float64(b.N), "http_us/op")
		})
	}
}
