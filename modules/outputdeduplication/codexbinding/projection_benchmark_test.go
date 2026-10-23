package codexbinding

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	expect "github.com/yusing/goutils/testing"
	"github.com/yusing/mekugi/modules/outputdeduplication/internal/projection"
)

func BenchmarkApplyProjectionSavings(b *testing.B) {
	for _, tc := range []struct {
		name             string
		instructionBytes int
	}{
		{"SmallInput", 64},
		{"LargeUnchangedInstruction", 256 << 10},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := strings.Repeat("short repeated tool output with enough bytes for indexing\n", 4)
			const instruction = "unchanged instruction text. "
			input, err := json.Marshal([]map[string]any{
				{"role": "developer", "content": strings.Repeat(instruction, tc.instructionBytes/len(instruction)+1)[:tc.instructionBytes]},
				{"type": "function_call_output", "call_id": "source", "output": body},
				{"type": "function_call_output", "call_id": "repeat", "output": body},
			})
			expect.NoError(b, err)
			// Exclude one-time tokenizer initialization from request-local costs.
			apply(projection.Service{}, input, duplicateOutputHostParts(input))
			b.ReportAllocs()
			for b.Loop() {
				// Applying consumes the host-part bounds, so each request needs its own.
				result := apply(projection.Service{}, input, duplicateOutputHostParts(input))
				if result.Receipt == nil || !result.Receipt.TokensMeasured || result.Receipt.OutputRepetitions != 1 || result.Receipt.SavedTokens <= 0 {
					b.Fatalf("unexpected projection receipt: %+v", result.Receipt)
				}
			}
		})
	}
}
