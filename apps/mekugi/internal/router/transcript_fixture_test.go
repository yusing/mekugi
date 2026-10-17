package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

// applyTranscriptFixture feeds the real projector and reducer for view fixtures
// that do not need an application control session.
func applyTranscriptFixture(v *activityAdapter, live bool, cwd, main, thread, turn, id, method, delta string, item binding.ThreadItem) {
	if item.Type == "commandExecution" {
		v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: v.LastSeq + 1, Agent: "Main", Kind: "command", Text: item.Text, Observed: v.Now(), Native: &sessionpresentation.NativeItem{
			Thread: thread, Turn: turn, Item: id, Phase: method, Live: live, Command: item.Command, Status: item.Status, Exit: item.ExitCode, Duration: sessionpresentation.ItemDuration(item.Observation())}}}})
		return
	}
	event := observation.TextEvent{At: v.Now(), Kind: observation.Kind(method), Delta: delta, Item: transcriptItem(item, thread, turn, id)}
	var state transcript.State
	var threadStore, turnStore scope.Store
	updates := state.Observe(event, true, observation.TextScope{Thread: &threadStore, Turn: &turnStore})
	for _, update := range updates {
		update.Live = live
		v.ApplyTranscript(update, item.Content, cwd, main, item.Type == "userMessage")
	}
	// Deliver the semantic transition before terminal input consults its caches.
	v.publishActivity()
}
