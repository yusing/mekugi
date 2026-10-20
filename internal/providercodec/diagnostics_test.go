package providercodec

import (
	"testing"
)

func TestProviderBoundarySuppliedDiagnostics(t *testing.T) {
	policy := testPolicy()
	policy.Diagnostics.Name = "Fixture Provider"
	policy.Diagnostics.Subject = "fixture route"
	policy.Diagnostics.Prefix = "fixture"
	t.Run("validator", func(t *testing.T) {
		request, err := testParse(mustTestJSON(t, map[string]any{
			"model": "fixture-model", "input": []any{}, "previous_response_id": "old",
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = Translate(request, "fixture-model", "chat", policy)
		if err == nil || err.Error() != "fixture route requires explicit conversation history, not previous_response_id" {
			t.Fatalf("validator ignored supplied provider diagnostics: %v", err)
		}
	})
	t.Run("decoder", func(t *testing.T) {
		tr := &Stream{policy: policy, endpoint: chatEndpoint{}}
		assertCodecFailure(t, tr, "data: not-json\n\n", "fixture_stream_invalid_json", "invalid JSON in Fixture Provider response stream")
	})
	t.Run("shared emitter", func(t *testing.T) {
		tr := &Stream{policy: policy, endpoint: chatEndpoint{}}
		stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}})
		assertCodecFailure(t, tr, stream, "fixture_stream_missing_calls", "Fixture Provider finished tool calls without a call")
	})
}
