package sessionpresentation

import (
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestNativeCompactionScalarOverridesPartialLiveAndRestoredItems(t *testing.T) {
	var attachment Attachment
	attachment.Start("child")
	attachment.RestoreCompactions("child", []string{"retained-item"})
	attachment.ObserveNativeMetrics("child", observation.SessionMetrics{Compactions: new(uint64(19))})
	attachment.ObserveCompaction("child", "live-item")
	attachment.RestoreCompactions("child", []string{"another-retained-item"})
	if got := attachment.InspectionMetrics("child"); got.Compactions == nil || *got.Compactions != 19 {
		t.Fatalf("Go item recount replaced native history scalar: %+v", got)
	}
	attachment.ObserveNativeMetrics("child", observation.SessionMetrics{})
	if attachment.InspectionMetrics("child").Compactions != nil {
		t.Fatal("unavailable native metric became a partial Go count")
	}
}
