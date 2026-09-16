package capturer

import (
	"fmt"
	"testing"
)

func TestMekugiCarrierOutcomesRequireEvidence(t *testing.T) {
	for _, kind := range []string{"", "other", "exec_command", "future_kind"} {
		t.Run(kind, func(t *testing.T) {
			total := mekugiMetrics{Diagnostics: make(map[string]uint64)}
			var provider, delivered []toolCallMetrics
			for i := range 21 {
				id := fmt.Sprintf("edit-%d", i)
				provider = append(provider, toolCallMetrics{CallID: id, Name: "hpatch", InputTokens: 3})
				carrier := toolCallMetrics{CallID: id, Name: "exec", Kind: kind, InputTokens: 9}
				if i < 2 {
					carrier.Kind, carrier.Diagnostic = "mekugi_diagnostic", "row-stale"
				}
				delivered = append(delivered, carrier)
			}
			addMekugi(&total, provider, delivered)
			if total.Calls != 21 || total.Rejected != 2 || total.Unclassified != 19 ||
				total.Successful != 0 || total.Unmatched != 0 || total.Diagnostics["row-stale"] != 2 {
				t.Fatalf("unknown carriers became outcomes: %+v", total)
			}
			if total.ProviderInputTokens != 63 || total.DeliveredInputTokens != 189 ||
				total.CarrierInputTokensExpansion != 126 {
				t.Fatalf("classification changed token accounting: %+v", total)
			}
		})
	}
}

func TestMekugiCarrierDeliveryDoesNotRequireHostConfirmation(t *testing.T) {
	total := mekugiMetrics{Diagnostics: make(map[string]uint64)}
	addMekugi(&total, []toolCallMetrics{
		{CallID: "patch", Name: "hpatch"},
		{CallID: "report", Name: "hpatch_recover"},
		{CallID: "missing", Name: "hpatch"},
		{CallID: "shell", Name: "shell"},
	}, []toolCallMetrics{
		{CallID: "patch", Kind: "apply_patch"},
		{CallID: "report", Kind: "mekugi_report"},
		{CallID: "shell", Kind: "other"},
	})
	if total.Calls != 3 || total.Corrections != 1 || total.Successful != 2 ||
		total.Rejected != 0 || total.Unclassified != 0 || total.Unmatched != 1 {
		t.Fatalf("translation evidence was confused with host outcome: %+v", total)
	}
}
