package router

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalReadPaginationCountersPreserveRevision(t *testing.T) {
	t.Parallel()
	for _, view := range []string{"combined", "own", "tasks", "outline"} {
		for _, state := range []string{"working", "blocked", "done"} {
			for _, size := range []int{1, 8} {
				t.Run(view+"/"+state+"/"+strconv.Itoa(size), func(t *testing.T) {
					transform, proxy, _, workspace := newDurableTreeTransform(t)
					var mutations []journalMutation
					for range size {
						mutation := journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: &state}
						if size > 1 {
							mutation.Body = new(strings.Repeat("\x01", maxJournalItemBytes-64))
						}
						if state == "blocked" {
							mutation.Reason = new("Waiting for input")
						}
						mutations = append(mutations, mutation)
					}
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", mutations); err != nil {
						t.Fatal(err)
					}
					server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
					defer server.Close()
					token := proxy.commentary.subscribe(workspace+"\x00fixture", "read")
					proxy.commentary.bindActivity(token, transform.shellThreadID)
					sink := &httpCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
					var items []journalNode
					revision := ""
					pages := uint64(0)
					for page := 0; ; {
						payload, err := sink.send(t.Context(), map[string]any{"op": "read", "view": view, "page": page, "revision": revision})
						if err != nil {
							t.Fatalf("page %d: %v", page, err)
						}
						pages++
						var result struct {
							Items    []journalNode `json:"items"`
							Next     *int          `json:"next"`
							Revision string        `json:"revision"`
						}
						if err := json.Unmarshal(payload, &result); err != nil {
							t.Fatal(err)
						}
						if revision != "" && revision != result.Revision {
							t.Fatal("counter changed the read revision")
						}
						revision = result.Revision
						items = append(items, result.Items...)
						if result.Next == nil {
							break
						}
						page = *result.Next
						// Recover from durable records between pages, not the live cache.
						proxy.journals = newJournalStore()
					}
					if len(items) != size {
						t.Fatalf("read %d items, want %d", len(items), size)
					}
					counts, _ := journalTestCounters(t, proxy, workspace, transform.shellThreadID)
					if size > 1 && (view == "combined" || view == "own") && pages < 2 {
						t.Fatal("large escaped read did not exercise continuation")
					}
					if counts.Operations["read"] != pages {
						t.Fatalf("page measurements lost: %+v", counts)
					}
					// A real authored change must still reject an old revision.
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "set", P: "/1", Title: new("Changed")}}); err != nil {
						t.Fatal(err)
					}
					if _, err := sink.send(t.Context(), map[string]any{"op": "read", "view": view, "page": 1, "revision": revision}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
						t.Fatalf("authored change did not reject stale revision: %v", err)
					}
				})
			}
		}
	}
}

func TestJournalCounterWritesPreserveTaskTiming(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "durable"}[durable], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy, workspace := treeTestJournal(t)
				if !durable {
					proxy.replayStore = nil
					if err := proxy.journals.initialize(t.Context(), nil, workspace, "tree", "/root", ""); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := func() threadJournal {
					if durable {
						return treeSnapshot(t, proxy, workspace)
					}
					return proxy.journals.memory[journalKey(workspace, "tree")].clone()
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("working")})
				before := snapshot()
				time.Sleep(3 * time.Second)
				proxy.countJournalRead(t.Context(), workspace, "tree", "read-call", "read")
				proxy.countJournalRead(t.Context(), workspace, "tree", "read-call", "read")
				proxy.journalCounters(t.Context(), workspace, "tree", "", nil)
				after := snapshot()
				if after.Counters.Operations["read"] != 1 {
					t.Fatalf("counter receipt lost: %+v", after.Counters)
				}
				// Only the measurements and their bounded receipts may change.
				after.Counters, after.CounterReceipts = before.Counters, before.CounterReceipts
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("counter changed journal state:\nbefore %+v\nafter %+v", before, after)
				}
				if got := after.Items[0].WorkTimer.at(time.Now()); got != 3*time.Second {
					t.Fatalf("counter lost live elapsed time: %v", got)
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")})
				if got := journalTaskElapsed(snapshot().Items[0].node()); got != "3s" {
					t.Fatalf("work transaction lost elapsed time: %q", got)
				}
			})
		})
	}
}
