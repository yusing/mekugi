package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestTokenUsageServiceTierAcrossTransports(t *testing.T) {
	t.Parallel()
	for _, cacheWrites := range []uint64{0, 20_000} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ requested, served, want string }{
				{"priority", `"priority"`, "$0.9120"},
				{"fast", `"priority"`, "$0.9120"},
				{"priority", `"fast"`, "$0.9120"},
				{"priority", `"default"`, "$0.4560"},
				{"default", `"priority"`, "$0.9120"},
				{"auto", `"priority"`, "$0.9120"},
				{"", `"priority"`, "$0.9120"},
				{"priority", "", "$0.9120"},
				{"fast", "", "$0.9120"},
				{"auto", "", "n/a"},
				{"priority", `null`, "n/a"},
				{"priority", `42`, "n/a"},
				{"priority", `""`, "n/a"},
				{"priority", `"unpriced"`, "n/a"},
			} {
				t.Run(fmt.Sprintf("writes=%d/stream=%t/%s/%s", cacheWrites, stream, tc.requested, tc.served), func(t *testing.T) {

					proxy := newManagedMekugiProxy(t)
					request := serverRequest(t, func(fields map[string]any) {
						fields["model"], fields["stream"] = "gpt-5.6-sol", stream
						if tc.requested != "" {
							fields["service_tier"] = tc.requested
						}
					})
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
					body := map[string]any{
						"id": "tier-response", "status": "completed",
						"output": []any{map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}},
						"usage":  map[string]any{"input_tokens": 100_000, "input_tokens_details": map[string]any{"cached_tokens": 40_000, "cache_write_tokens": cacheWrites}, "output_tokens": 10_000, "output_tokens_details": map[string]any{"reasoning_tokens": 5_000}},
					}
					if tc.served != "" {
						body["service_tier"] = json.RawMessage(tc.served)
					}
					wire := string(mustTestJSON(t, body))
					if stream {
						wire = finalAnswerTestWire(append(finalAnswerTestEvents(t, "final_answer"), mustTestJSON(t, map[string]any{"type": "response.completed", "response": body})))
					}
					response := serverHTTPResponse(wire)
					if stream {
						response.Header.Set("Content-Type", "text/event-stream")
					}
					provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
					var output bytes.Buffer
					if err := executeRequest(t.Context(), t.Context(), request, headers, "tier-session", provider, &output, nil, proxy); err != nil {
						t.Fatal(err)
					}
					modelLabel := "gpt-5.6-sol high"
					if tc.served == `"fast"` || tc.served == `"priority"` || tc.served == "" && (tc.requested == "fast" || tc.requested == "priority") {
						modelLabel += " [fast]"
					}
					if strings.Contains(output.String(), "Router session usage") {
						t.Fatalf("completion emitted usage commentary: %s", output.String())
					}
					report, ok := proxy.usage.Snapshot("thread-1")
					want := 0.0
					priced := tc.want != "n/a"
					if priced {
						var err error
						want, err = strconv.ParseFloat(strings.TrimPrefix(tc.want, "$"), 64)
						if err != nil {
							t.Fatal(err)
						}
					}
					if cacheWrites != 0 {
						if want == .912 {
							want = .952
						} else if want == .456 {
							want = .476
						}
					}
					if !ok || report.Model != modelLabel || report.InputTokens != 100_000 || report.CacheWriteTokens != cacheWrites ||
						report.Cost.Known != priced || (priced && math.Abs(report.Cost.CachedInput+report.Cost.UncachedInput+report.Cost.Output-want) > 1e-10) {
						t.Fatalf("tier-aware usage = %+v, ok=%v", report, ok)
					}
					requested := tc.requested
					if requested == "fast" {
						requested = "priority"
					}
					var forwarded map[string]json.RawMessage
					if json.Unmarshal(provider.forwarded[0], &forwarded) != nil || responses.StringField(forwarded, "service_tier") != requested {
						t.Fatal("accounting changed requested tier")
					}
				})
			}
		}
	}
}
