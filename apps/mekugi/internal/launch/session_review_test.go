package launch

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	preview "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestReviewReceiptsWithoutTerminal(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "preview", true: "retained"}[retained], func(t *testing.T) {
			r := sessionreview.New(t.Context(), nil, nil)
			defer r.Close()
			u := &appServerUI{review: r, reviewSource: new(changecapture.Service)}
			for _, target := range []**sessionpresentation.ActivitySource{&u.attached.source, &u.attached.childSource} {
				*target = &sessionpresentation.ActivitySource{Model: new(activity.Model[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent])}
				(*target).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{
					Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `write source.go`", CallID: "edit",
					Native: &sessionpresentation.NativeItem{Thread: "main", Item: "edit", Phase: "item/completed"},
				}}})
			}
			r.IncludeThread("/workspace", "main", true)
			sub := r.Subscribe()
			defer r.Unsubscribe(sub)
			if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sub, <-sub.Events()) {
				t.Fatal("storage-free scope admission requested a resync")
			}
			const text = "Edit `source.go` +1 -1"
			if retained {
				r.Projection.Data = reviewReceiptSnapshot(t, []*changepresentation.Receipt{{Thread: "main", Calls: []string{"edit"}, Text: text}})
				publishReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane)
				applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
			} else {
				p := preview.Preview{ID: "observed", Workspace: "/workspace", Thread: "main", HostItems: []string{"edit"}, Complete: true, EditReceipt: text}
				admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sub, sessionreview.Event{Kind: "preview", Preview: &p})
			}
			for _, source := range []*sessionpresentation.ActivitySource{u.attached.source, u.attached.childSource} {
				record := source.Entries[0]
				if record.Source.Native.CapturedEdit == nil || record.Source.Text != text || record.Values[0].Verb != "Edit" {
					t.Fatalf("receipt depended on a renderer: %+v", record)
				}
			}
			if terminalTest(u) != nil || len(u.pendingEditReceipts) != 0 {
				t.Fatal("receipt reconciliation constructed a terminal or retained a matched preview")
			}
		})
	}
}
