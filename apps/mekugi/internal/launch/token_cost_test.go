package launch

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestFormatUsageTokens(t *testing.T) {
	for value, want := range map[uint64]string{0: "0", 12: "12", 123: "123", 1234: "1.2K", 149000: "149K", 1234567: "1.2M", 1200000000: "1.2B", ^uint64(0): "18446744073.7B"} {
		if got := activityui.FormatUsageTokens(value); got != want {
			t.Errorf("%d: %q != %q", value, got, want)
		}
	}
}

func TestTokenCostIncludesCompactionAcrossTransports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		stream      bool
		invalid     string
		serviceTier string
	}{
		{"json", false, "", ""},
		{"sse", true, "", ""},
		{"json-priority", false, "", "priority"},
		{"sse-fast", true, "", "fast"},
		{"json-excess-cache", false, "cache", ""},
		{"sse-excess-cache", true, "cache", ""},
		{"json-excess-reasoning", false, "reasoning", ""},
		{"sse-excess-reasoning", true, "reasoning", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {

			proxy := newManagedMekugiProxy(t)
			for step, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
				requestStream := tc.stream || step == 0 // Compaction requires streaming.
				request := serverRequest(t, func(fields map[string]any) {
					fields["model"], fields["stream"] = model, requestStream
					if tc.serviceTier != "" {
						fields["service_tier"] = tc.serviceTier
					}
					if step == 0 {
						delete(fields, "tools")
						fields["input"] = []any{map[string]any{"role": "user", "content": "compact"}}
						fields["parallel_tool_calls"] = false
					}
				})
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				if step == 0 {
					headers = serverCompactionMetadataHeaders(t)
					headers.Set(codexresponses.ThreadIDHeader, "thread-1")
				}
				body := map[string]any{
					"id": model, "status": "completed", "output": []any{},
					"usage": map[string]any{
						"input_tokens": 200_000, "input_tokens_details": map[string]any{"cached_tokens": 100_000},
						"output_tokens": 10_000, "output_tokens_details": map[string]any{"reasoning_tokens": 5_000},
					},
				}
				if step == 1 {
					usage := body["usage"].(map[string]any)
					switch tc.invalid {
					case "cache":
						usage["input_tokens_details"] = map[string]any{"cached_tokens": 300_000}
					case "reasoning":
						usage["output_tokens_details"] = map[string]any{"reasoning_tokens": 20_000}
					}
				}

				if step == 1 && !requestStream {
					body["output"] = []any{map[string]any{
						"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed",
						"content": []any{map[string]any{"type": "output_text", "text": "No files were changed."}},
					}}
				}
				wire := string(mustTestJSON(t, body))
				if requestStream {
					var events [][]byte
					if step == 1 {
						events = finalAnswerTestEvents(t, "final_answer")
					}
					events = append(events, mustTestJSON(t, map[string]any{"type": "response.completed", "response": body}))
					wire = finalAnswerTestWire(events)
				}
				response := serverHTTPResponse(wire)
				if requestStream {
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, model, provider, &output, nil, proxy); err != nil {
					t.Fatal(err)
				}
				if step == 0 {
					if output.String() != wire {
						t.Fatal("compaction output changed")
					}
					continue
				}
				if strings.Contains(output.String(), "Router session usage") {
					t.Fatalf("completion emitted usage commentary: %s", output.String())
				}
				report, ok := proxy.usage.Snapshot("thread-1")
				wantCost := 2.24
				if tc.serviceTier != "" {
					wantCost = 4.48
				}
				if !ok || report.InputTokens != 400_000 || report.Cost.Known != (tc.invalid == "") ||
					(tc.invalid == "" && math.Abs(report.Cost.CachedInput+report.Cost.UncachedInput+report.Cost.Output-wantCost) > 1e-10) {
					t.Fatalf("compaction usage = %+v, ok=%v", report, ok)
				}
				if !strings.Contains(output.String(), "No files were changed.") {
					t.Fatal("provider final text was filtered")
				}
			}
		})
	}
}
