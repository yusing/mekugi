package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (u *appServerUI) holdCommand(entry sessionpresentation.ActivityEntry, frame activityui.CommandFrame) {
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.HoldCommand(seq, frame)
		}
	}
}

func (u *appServerUI) deliverObservation(deliveries []sessionpresentation.ActivityDelivery) {
	for _, delivery := range deliveries {
		switch {
		case delivery.Text != nil:
			for _, update := range u.streams.Reasoning.Queue([]observation.TextUpdate{*delivery.Text}) {
				u.holdReasoning(update)
			}
		case delivery.Command != nil:
			u.streams.DeliverCommand(*delivery.Command, u.view, u.agents)
		case delivery.Rename != nil:
			u.renameActivitySelection(delivery.Rename.Old, delivery.Rename.Name)
		}
	}
}

func (u *appServerUI) releaseCommand(entry sessionpresentation.ActivityEntry) {
	at := u.now()
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.ReleaseCommand(seq, at)
		}
	}
}
