package router

import (
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/transcript"
)

// applyTranscriptFixture feeds the real projector and reducer for view fixtures
// that do not need an application control session.
func applyTranscriptFixture(v *activityAdapter, live bool, cwd, main, thread, turn, id, method, delta string, item appServerItem) {
	if item.Type == "commandExecution" {
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.LastSeq + 1, Agent: "Main", Kind: "command", Text: item.Text, Observed: v.Now(), native: &liveActivityNativeItem{
			thread: thread, turn: turn, item: id, phase: method, live: live, command: item.Command, status: item.Status, exit: item.ExitCode, duration: appServerDuration(item)}}}})
		return
	}
	event := observation.TextEvent{At: v.Now(), Kind: observation.Kind(method), Delta: delta, Item: transcriptItem(item, thread, turn, id)}
	var state transcript.State
	var threadStore, turnStore scope.Store
	updates := state.Observe(event, true, observation.TextScope{Thread: &threadStore, Turn: &turnStore})
	for _, update := range updates {
		update.Live = live
		v.applyTranscript(update, item.Content, cwd, main, item.Type == "userMessage")
	}
}
