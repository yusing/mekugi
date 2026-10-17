package router

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func appServerDuration(item observation.Item) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

func activityContent(entry activityPaneEntry) activitypresentation.ItemContent {
	content := activitypresentation.ItemContent{
		Facts:   activitypresentation.Facts{Seq: entry.Seq, Agent: entry.Agent, Kind: entry.Kind, Text: entry.Text},
		Command: commandPresentation(entry), ErrorDetail: entry.ErrorDetail,
	}
	if message := entry.message; message != nil {
		content.Message = &activitypresentation.DirectedContent{From: message.from, To: message.to, Text: message.text}
	}
	if assignment := entry.assignment; assignment != nil {
		content.Assignment = &activitypresentation.DirectedContent{From: assignment.from, To: assignment.to, Text: assignment.text}
	}
	if entry.start != nil {
		content.StartLabel = new(entry.start.label())
	}
	if n := entry.native; n != nil {
		content.Details = &activitypresentation.ItemDetails{
			CommandText: n.command, Status: n.status, Timeout: n.commandTimeout,
			ReasoningLive: n.phase == "summary" || n.phase == "item/started", WaitLive: n.live && n.phase == "item/started",
			Workdir: n.workdir, Output: retainedOutputID(n.output, 1),
			SearchResults: n.searchResults, Questions: n.questions, Attachments: n.attachments,
			Wait: n.wait, Hook: n.hook, Thought: n.thought,
		}
	}
	return content
}

// instantOperations reports tool text whose every operation prints what it
// reads at once, so its output shows whole rather than rolling through.
func instantOperations(text string) bool {
	blocks := activitypresentation.Operations(textsanitize.Safe(text, false))
	return len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block activitypresentation.Value) bool { return !block.Instant() })
}
