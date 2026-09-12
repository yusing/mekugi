package router

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestUsageEvidencePresence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, field := range []string{"input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens", "cache_write_tokens"} {
			for _, state := range []string{"present", "missing", "null", "negative", "fractional", "overflow"} {
				t.Run(fmt.Sprintf("stream=%t/%s/%s", stream, field, state), func(t *testing.T) {
					usage := map[string]any{
						"input_tokens": 0, "output_tokens": 0,
						"input_tokens_details":  map[string]any{"cached_tokens": 0, "cache_write_tokens": 0},
						"output_tokens_details": map[string]any{"reasoning_tokens": 0},
					}
					owner := usage
					switch field {
					case "cached_tokens", "cache_write_tokens":
						owner = usage["input_tokens_details"].(map[string]any)
					case "reasoning_tokens":
						owner = usage["output_tokens_details"].(map[string]any)
					}
					switch state {
					case "missing":
						delete(owner, field)
					case "null":
						owner[field] = nil
					case "negative":
						owner[field] = -1
					case "fractional":
						owner[field] = 1.5
					case "overflow":
						owner[field] = json.RawMessage(`18446744073709551616`)
					}
					var payload any = map[string]any{"usage": usage}
					if stream {
						payload = map[string]any{"type": "response.completed", "response": payload}
					}
					counts, observed := usageFromResponsePayload(mustTestJSON(t, payload), stream)
					wantObserved := state == "present" || state == "missing" || state == "null" || field == "cache_write_tokens"
					wantIncomplete := state == "null" || state == "missing" && field != "cache_write_tokens" || field == "cache_write_tokens" && state != "present" && state != "missing"
					if observed != wantObserved || observed && counts.Incomplete != wantIncomplete {
						t.Fatalf("observed=%t counts=%+v", observed, counts)
					}
				})
			}
		}
	}
}

func TestUsageNullAndNonterminalAreNotObserved(t *testing.T) {
	for _, raw := range []string{`{}`, `{"usage":null}`, `{"usage":[]}`, `{"usage":"unknown"}`} {
		for _, stream := range []bool{false, true} {
			payload := []byte(raw)
			if stream {
				payload = mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(raw)})
			}
			if got, ok := usageFromResponsePayload(payload, stream); ok {
				t.Fatalf("observed absent usage: %s %+v", payload, got)
			}
		}
	}
	for _, kind := range []string{"response.created", "response.in_progress"} {
		payload := mustTestJSON(t, map[string]any{"type": kind, "response": map[string]any{"usage": map[string]any{"input_tokens": 100}}})
		if got, ok := usageFromResponsePayload(payload, true); ok {
			t.Fatalf("observed nonterminal usage: %+v", got)
		}
	}
}

func TestUsageCacheWrites(t *testing.T) {
	for _, writes := range []uint64{0, 4, 5} {
		payload := mustTestJSON(t, map[string]any{"usage": map[string]any{
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 6, "cache_write_tokens": writes},
			"output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
		}})
		got, ok := usageFromResponsePayload(payload, false)
		want := tokenCounts{InputTokens: 10, UncachedInputTokens: 4, CacheWriteTokens: writes, OutputTokens: 4, ReasoningTokens: 3, Inconsistent: writes > 4}
		if !ok || got != want {
			t.Fatalf("counts=%+v valid=%t want=%+v", got, ok, want)
		}
	}
}
