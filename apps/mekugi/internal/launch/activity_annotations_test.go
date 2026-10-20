package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestCapturedEditRetainsOutputAnnotations(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		for _, filterFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("withdraw=%t/filterFirst=%t", withdraw, filterFirst), func(t *testing.T) {
				v := newActivityAdapter()
				v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: "Run `printf output > source.go`", OutputTail: []string{"output"}, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd", Phase: "item/started", Command: "printf output > source.go", Running: true, Live: true}}}})
				receipt := &changepresentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `source.go` +1 -1"}
				if withdraw {
					v.ApplyCapturedEdits([]*changepresentation.Receipt{receipt})
				}
				note := func() {
					if !v.MarkUnreturned("main", "cmd") {
						t.Fatal("output note was not admitted")
					}
				}
				if filterFirst {
					note()
				}
				v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 2, Kind: "output_dedupe", CallID: "cmd", Text: "Repeated fixture output", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd"}}}})
				if !filterFirst {
					note()
				}
				next := *receipt
				if withdraw {
					next.Text = ""
				}
				for pass := range 2 {
					v.ApplyCapturedEdits([]*changepresentation.Receipt{&next})
					for _, approve := range []bool{false, true} {
						if approve {
							v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(3 + pass), Agent: "Main", Kind: "tool", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd", Phase: "approval/updated", Approval: "Approved"}}}})
						}
						frame := ansi.Strip(strings.Join(v.RenderFeed(100, 30).Lines, "\n"))
						if strings.Count(frame, "Repeated fixture output") != 1 || strings.Count(frame, "output not returned to the model") != 1 {
							t.Fatalf("approval=%v: reparse lost or duplicated notes:\n%s", approve, frame)
						}
					}
				}
			})
		}
	}
}
