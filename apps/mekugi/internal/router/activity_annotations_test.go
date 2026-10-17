package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestCapturedEditRetainsOutputAnnotations(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		for _, filterFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("withdraw=%t/filterFirst=%t", withdraw, filterFirst), func(t *testing.T) {
				v := newActivityAdapter()
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: "Run `printf output > source.go`", outputTail: []string{"output"}, native: &liveActivityNativeItem{thread: "main", item: "cmd", phase: "item/started", command: "printf output > source.go", running: true, live: true}}}})
				receipt := &changepresentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `source.go` +1 -1"}
				if withdraw {
					v.applyCapturedEdits([]*changepresentation.Receipt{receipt})
				}
				note := func() {
					if !v.markUnreturned("main", "cmd") {
						t.Fatal("output note was not admitted")
					}
				}
				if filterFirst {
					note()
				}
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Kind: "output_dedupe", CallID: "cmd", Text: "Repeated fixture output", native: &liveActivityNativeItem{thread: "main", item: "cmd"}}}})
				if !filterFirst {
					note()
				}
				next := *receipt
				if withdraw {
					next.Text = ""
				}
				for pass := range 2 {
					v.applyCapturedEdits([]*changepresentation.Receipt{&next})
					for _, approve := range []bool{false, true} {
						if approve {
							v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(3 + pass), Agent: "Main", Kind: "tool", native: &liveActivityNativeItem{thread: "main", item: "cmd", phase: "approval/updated", approval: "Approved"}}}})
						}
						frame := ansi.Strip(strings.Join(v.RenderFeed(100, 30).Lines, "\n"))
						if strings.Count(frame, "Repeated fixture output") != 1 || strings.Count(frame, unreturnedOutputNote) != 1 {
							t.Fatalf("approval=%v: reparse lost or duplicated notes:\n%s", approve, frame)
						}
					}
				}
			})
		}
	}
}
