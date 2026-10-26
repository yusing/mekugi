package presentation

import "testing"

func TestTemporaryEditsKeepUnsettledWorkVisible(t *testing.T) {
	t.Parallel()
	for _, pending := range []bool{false, true} {
		value := Value{Kind: "op", Verb: "Create", Label: "`/tmp/tree/a.go` +1 -0", GroupHeader: "Edited", Running: !pending, Pending: pending}
		if TemporaryEditRun([]Value{value}) != 0 {
			t.Fatalf("unsettled edit eligible for folding: %+v", value)
		}
	}
}
