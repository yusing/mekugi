package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalTransportWaitsForCompletionOrCallerCancellation(t *testing.T) {
	for _, scenario := range []string{"delivery lease", "committed response", "caller cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				replay, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				store := newJournalStore()
				if err := store.initialize(t.Context(), replay, "workspace", "thread", "/root", ""); err != nil {
					t.Fatal(err)
				}
				before, _, err := readThreadJournal(replay, "workspace", "thread")
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "committed response" {
					// An independent router holds the shared delivery lease until
					// its downstream write is confirmed, longer than the old timer.
					release, err := newJournalStore().lockDelivery(t.Context(), replay)
					if err != nil {
						t.Fatal(err)
					}
					released := make(chan struct{})
					defer func() { <-released }()
					go func() {
						time.Sleep(3 * time.Second)
						release()
						close(released)
					}()
				}
				broker := newCommentaryBroker()
				broker.journalPublisher = func(ctx context.Context, session, thread, receipt string, mutations []journalMutation) ([]string, error) {
					if session != "session" || thread != "thread" || receipt != "receipt" {
						t.Errorf("publication identity = %q, %q, %q", session, thread, receipt)
					}
					return store.apply(ctx, replay, "workspace", thread, receipt, mutations)
				}
				token := broker.subscribe("session", "call")
				broker.bindActivity(token, "thread")
				client := *commentaryHTTPClient
				attempts := 0
				// Keep the production HTTP client and authenticated handler, but
				// replace sockets so lock waits and deadlines use controlled time.
				client.Transport = serverRoundTripper(func(request *http.Request) (*http.Response, error) {
					attempts++
					recorder := httptest.NewRecorder()
					broker.serveHTTP(recorder, request)
					if scenario == "committed response" {
						select {
						case <-time.After(3 * time.Second):
						case <-request.Context().Done():
						}
					}
					if err := request.Context().Err(); err != nil {
						return nil, err
					}
					return recorder.Result(), nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if scenario == "caller cancellation" {
					go func() {
						time.Sleep(time.Second)
						cancel()
					}()
				}
				sink := httpCommentarySink{endpoint: "http://journal.test/internal/commentary", token: token, client: &client}
				result, sendErr := sink.send(ctx, map[string]any{
					"id": "receipt", "journal": jsontext.Value(`{"op":"add","title":"Persisted once"}`),
				})
				after, _, err := readThreadJournal(replay, "workspace", "thread")
				if err != nil {
					t.Fatal(err)
				}
				if attempts != 1 {
					t.Fatalf("publication attempted %d times", attempts)
				}
				if scenario == "caller cancellation" {
					if !errors.Is(sendErr, context.Canceled) || after.Sequence != before.Sequence || len(after.Receipts) != len(before.Receipts) {
						t.Fatalf("cancellation: error=%v, sequence=%d, receipts=%d", sendErr, after.Sequence, len(after.Receipts))
					}
					return
				}
				if sendErr != nil {
					t.Fatal(sendErr)
				}
				var response struct {
					OK    bool     `json:"ok"`
					Items []string `json:"items"`
				}
				if err := json.Unmarshal(result, &response); err != nil {
					t.Fatal(err)
				}
				if !response.OK || len(response.Items) != 1 || response.Items[0] != "/1" ||
					after.Sequence != before.Sequence+1 || len(after.Receipts["receipt"].IDs) != 1 ||
					len(after.Items) != 1 || after.Items[0].Title != "Persisted once" {
					t.Fatalf("publication not confirmed exactly once: response=%+v, journal=%+v", response, after)
				}
			})
		})
	}
}
