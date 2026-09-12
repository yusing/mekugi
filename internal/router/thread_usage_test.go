package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestThreadUsageConcurrentObservationsAndDuplicateTerminals(t *testing.T) {
	totals := newThreadUsage()
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			thread := fmt.Sprint(i)
			for range 100 {
				observation := totals.observation(thread, "", "gpt-5.5", "")
				var duplicates sync.WaitGroup
				for range 3 {
					duplicates.Go(func() {
						observation.observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
					})
				}
				duplicates.Wait()
			}
		})
	}
	workers.Wait()
	for i := range 8 {
		got, valid := totals.snapshot(fmt.Sprint(i))
		if !valid || got.tokenCounts != (tokenCounts{InputTokens: 1000, UncachedInputTokens: 400, OutputTokens: 300, ReasoningTokens: 200}) {
			t.Fatalf("thread %d: counts=%+v valid=%v", i, got, valid)
		}
	}
}
func TestThreadUsageCapacityOverflowAndShutdownFailAuxiliary(t *testing.T) {
	totals := newThreadUsage()
	for i := range maxCommentaryRoutes {
		totals.observation(fmt.Sprint(i), "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 1})
	}
	totals.observation("excess", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 100})
	if _, valid := totals.snapshot("excess"); valid {
		t.Fatal("capacity admitted a new identity")
	}
	totals.observation("0", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 2})
	if got, valid := totals.snapshot("0"); !valid || got.InputTokens != 3 {
		t.Fatal("capacity lost or stopped an existing total", got, valid)
	}
	totals.observation("1", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: ^uint64(0)})
	if _, valid := totals.snapshot("1"); valid || totals.threads["1"].counts.InputTokens != 1 {
		t.Fatal("overflow published or replaced the retained total")
	}
	totals.observation("1", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 1})
	if _, valid := totals.snapshot("1"); valid {
		t.Fatal("overflowed thread resumed with an incomplete total")
	}
	totals.close()
	totals.observation("0", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 1})
	if _, valid := totals.snapshot("0"); valid || len(totals.threads) != 0 {
		t.Fatal("closed accumulator retained or admitted usage")
	}
	for _, thread := range []string{"", strings.Repeat("x", maxCommentaryPublicationBytes+1)} {
		fresh := newThreadUsage()
		fresh.observation(thread, "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 1})
		if len(fresh.threads) != 0 {
			t.Fatal("unbounded identity admitted")
		}
	}
}

func TestThreadUsageSurvivesRoundTripsAndSessionRemapping(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	first, _ := prepareActivityTest(t, proxy, "session-a", "root-thread", "", "/root", nil)
	first.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.Close()
	child, _ := prepareActivityTest(t, proxy, "session-a", "child-thread", "root-thread", "/root/child", nil)
	child.observeResponseUsage(tokenCounts{InputTokens: 500})
	next, _ := prepareActivityTest(t, proxy, "session-remapped", "root-thread", "", "/root", nil)
	second := tokenCounts{InputTokens: 20, UncachedInputTokens: 5, OutputTokens: 7, ReasoningTokens: 6}
	next.observeResponseUsage(second)
	got, valid := next.threadUsageCounts()
	if !valid || got.tokenCounts != (tokenCounts{InputTokens: 30, UncachedInputTokens: 9, OutputTokens: 10, ReasoningTokens: 8}) {
		t.Fatal("root lifetime counts changed across round trips", got, valid)
	}

	if got, valid := child.threadUsageCounts(); !valid || got.InputTokens != 500 {
		t.Fatal("root and child totals mixed", got, valid)
	}
	next.Close()
	later, _ := prepareActivityTest(t, proxy, "later-session", "root-thread", "", "/root", nil)
	if laterCounts, valid := later.threadUsageCounts(); !valid || laterCounts != got {
		t.Fatal("completed response lifetime reset totals", laterCounts, valid)
	}
}

func TestThreadUsageIncludesCompactionWithoutRewritingIt(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	first, _ := prepareActivityTest(t, proxy, "before", "root-thread", "", "/root", nil)
	first.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.Close()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{map[string]any{"type": "message", "role": "user", "content": "compact this"}},
		"stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
	}))
	if err != nil {
		t.Fatal(err)
	}
	terminal := mustTestJSON(t, map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": "compaction", "status": "completed", "output": []any{},
			"usage": map[string]any{
				"input_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 5},
				"output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
			},
		},
	})
	responseBody := "event: response.completed\ndata: " + string(terminal) + "\n\n"
	response := serverHTTPResponse(responseBody)
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	headers := serverCompactionMetadataHeaders(t)
	headers.Set(threadIDHeader, "root-thread")
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compaction-session", provider, &output, nil, proxy, nil, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != responseBody {
		t.Fatal("compaction usage accounting rewrote provider output", output.String())
	}
	next, _ := prepareActivityTest(t, proxy, "after-remap", "root-thread", "", "/root", nil)
	next.observeResponseUsage(tokenCounts{InputTokens: 1, UncachedInputTokens: 1})
	if got, valid := next.threadUsageCounts(); !valid || got.tokenCounts != (tokenCounts{InputTokens: 23, UncachedInputTokens: 12, OutputTokens: 10, ReasoningTokens: 5}) {
		t.Fatal("compaction tokens missing from lifetime total", got, valid)
	}
	var forwarded map[string]json.RawMessage
	if len(provider.forwarded) != 1 || json.Unmarshal(provider.forwarded[0], &forwarded) != nil || !bytes.Equal(forwarded["input"], request.fields["input"]) {
		t.Fatal("compaction request changed")
	}
}
