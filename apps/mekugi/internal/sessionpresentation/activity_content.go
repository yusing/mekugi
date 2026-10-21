package sessionpresentation

import (
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/live_activity_blocks.go:19:43@45fbe279
func ActivityContent(entry ActivityEntry) activitypresentation.ItemContent {
	content := activitypresentation.ItemContent{
		Facts:   entry.ActivityFacts(),
		Command: CommandPresentation(entry), ErrorDetail: entry.ErrorDetail,
	}
	if message := entry.Message; message != nil {
		content.Message = &activitypresentation.DirectedContent{From: message.From, To: message.To, Text: message.Text}
	}
	if assignment := entry.Assignment; assignment != nil {
		content.Assignment = &activitypresentation.DirectedContent{From: assignment.From, To: assignment.To, Text: assignment.Text}
	}
	if entry.Start != nil {
		content.StartLabel = new(entry.Start.Label())
	}
	if n := entry.Native; n != nil {
		content.Details = &activitypresentation.ItemDetails{
			CommandText: n.Command, Status: n.Status, Timeout: n.CommandTimeout,
			ReasoningLive: n.Phase == "summary" || n.Phase == "item/started", WaitLive: n.Live && n.Phase == "item/started",
			Workdir: n.Workdir, Output: RetainedOutputID(n.Output, 1),
			SearchResults: n.SearchResults, Questions: n.Questions, Attachments: n.Attachments,
			Wait: n.Wait, Hook: n.Hook, Thought: n.Thought,
		}
	}
	return content
}
