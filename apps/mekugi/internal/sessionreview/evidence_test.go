package sessionreview

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"
)

func TestCapturedReceiptUsesHostIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		codeMode bool
		results  []legacycall.HostResult
		want     []string
	}{
		{"native shell", false, []legacycall.HostResult{{Tool: "userShell", CallID: "host-shell"}}, []string{"host-shell"}},
		{"code mode", true, []legacycall.HostResult{{Tool: "exec_command", CallID: "first"}, {Tool: "exec_command", CallID: "second"}}, []string{"first", "second"}},
		{"legacy", false, nil, []string{"retained"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := CapturedReceipt(t.TempDir(), legacycall.History{
				ExecutingThread: "thread", CorrelationID: "retained\x00exec",
				ExecOutcome: &legacycall.Outcome{CodeMode: test.codeMode}, HostResults: test.results,
			}, nil)
			if receipt == nil || !slices.Equal(receipt.Calls, test.want) {
				t.Fatalf("receipt = %+v, want calls %v", receipt, test.want)
			}
		})
	}
}
