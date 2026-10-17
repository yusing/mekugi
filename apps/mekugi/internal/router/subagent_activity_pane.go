package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func takeNativeActivity(a *sessioncontrol.Audience, root string) []sessionpresentation.ActivityEntry {
	audience, events := a.Take(root)
	var entries []sessionpresentation.ActivityEntry
	for _, event := range events {
		entry := sessionpresentation.ActivityEntry{Agent: audience.Threads[event.Thread], Kind: event.Kind, Text: event.Text, CallID: event.CallID, Observed: event.Observed}
		if event.Kind == "error" {
			entry.ErrorDetail, entry.Text = event.Text, activityui.ErrorPreview(event.Text)
		}
		if a := event.Assignment; a != nil {
			entry.Assignment = &sessionpresentation.ActivityAssignment{ID: a.ID, From: a.From, To: a.To, Text: a.Text, Created: a.Created}
		}
		if m := event.Message; m != nil {
			entry.Message = &sessionpresentation.ActivityMessage{From: m.From, To: m.To, Text: m.Text}
		}
		if s := event.Start; s != nil {
			entry.Start = &sessionpresentation.ActivityStart{Model: s.Model, Effort: s.Effort, Tier: s.Tier}
		}
		if event.Kind == "output_dedupe" {
			entry.Native = &sessionpresentation.NativeItem{Thread: event.Thread}
		}
		entries = append(entries, entry)
	}
	return entries
}
