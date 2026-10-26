package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func takeNativeActivity(a *sessioncontrol.Audience, root string) []sessionpresentation.ActivityEntry {
	audience, events := a.Take(root)
	var entries []sessionpresentation.ActivityEntry
	for _, event := range events {
		entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: event.Thread, Turn: event.Turn, Item: event.Source, Phase: "supplement", Live: true}, Agent: audience.Threads[event.Thread], Kind: event.Kind, Text: event.Text, CallID: event.CallID, Observed: event.Observed}
		if event.Thread == root {
			entry.Agent = "/root"
		}
		if event.Kind == "error" {
			entry.ErrorDetail, entry.Text = event.Text, activityui.ErrorPreview(event.Text)
		}
		if event.Kind == activitypresentation.SupplementEditReceipt {
			entry.Native.CapturedEdit = &changepresentation.Receipt{Thread: event.Thread, Calls: event.Calls, Text: event.Text}
		}
		entry.ReloadSources = event.ReloadSources
		entries = append(entries, entry)
	}
	return entries
}
