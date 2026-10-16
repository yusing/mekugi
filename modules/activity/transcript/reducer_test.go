package transcript_test

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/transcript"
)

func TestTranscriptPublicSummaryLifecycle(t *testing.T) {
	at := time.Unix(100, 0)
	ref := observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "summary"}
	var state transcript.State
	pending := state.ProviderStarted("child", at)
	if len(pending) != 1 || pending[0].Kind != "reasoning" || pending[0].Text != "" {
		t.Fatalf("pending: %+v", pending)
	}
	e := observation.TextEvent{At: at.Add(time.Second), Kind: observation.ItemStarted, Item: observation.TextItem{Ref: ref, Type: "reasoning"}}
	started := state.Observe(e, false)
	if len(started) != 1 || started[0].Replaces != pending[0].Ref.ItemID {
		t.Fatalf("takeover: %+v", started)
	}
	e.Kind, e.Delta = observation.ReasoningDelta, "First"
	state.Observe(e, false)
	e.Kind = observation.ReasoningPart
	state.Observe(e, false)
	e.Kind = observation.ReasoningDelta
	e.Delta = "Second"
	streamed := state.Observe(e, false)
	if len(streamed) != 1 || streamed[0].Text != "First\n\nSecond" {
		t.Fatalf("parts: %+v", streamed)
	}
	e.Kind, e.At = observation.ItemCompleted, at.Add(4*time.Second)
	done := state.Observe(e, false)
	if len(done) != 1 || done[0].Text != "First\n\nSecond" || done[0].Thought != 4*time.Second {
		t.Fatalf("completion lost public stream/start: %+v", done)
	}
	e.Kind = observation.ReasoningDelta
	if late := state.Observe(e, false); len(late) != 0 {
		t.Fatalf("late delta reopened summary: %+v", late)
	}
	if ended := state.TurnCompleted("child", "turn", "completed", e.At, true); len(ended) != 0 {
		t.Fatalf("completed summary repeated: %+v", ended)
	}
}

func TestTranscriptTurnEndAndOtherOutputRetireThinking(t *testing.T) {
	at := time.Unix(100, 0)
	var state transcript.State
	state.ProviderStarted("child", at)
	if dropped := state.OtherItemStarted("child"); len(dropped) != 1 || dropped[0].Phase != "discarded" {
		t.Fatalf("other output: %+v", dropped)
	}
	ref := observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "summary"}
	e := observation.TextEvent{At: at, Kind: observation.ItemStarted, Item: observation.TextItem{Ref: ref, Type: "reasoning"}}
	if updates := state.Observe(e, false); len(updates) != 0 {
		t.Fatalf("empty start became visible: %+v", updates)
	}
	done := state.TurnCompleted("child", "turn", "interrupted", at.Add(time.Second), true)
	if len(done) != 1 || done[0].Phase != "discarded" {
		t.Fatalf("empty turn end: %+v", done)
	}
	e.Kind = observation.ReasoningDelta
	if late := state.Observe(e, false); len(late) != 0 {
		t.Fatalf("interrupted summary reopened: %+v", late)
	}
}

func TestTranscriptChildFinalIsScopedToCompletedTurn(t *testing.T) {
	var state transcript.State
	e := observation.TextEvent{At: time.Unix(100, 0), Kind: observation.ItemCompleted, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "message"}, Type: "agentMessage", Text: "Answer"}}
	state.TurnStarted("child")
	if message := state.Observe(e, false); len(message) != 1 || message[0].Kind != "text" {
		t.Fatalf("message: %+v", message)
	}
	final := state.TurnCompleted("child", "turn", "completed", e.At, true)
	if len(final) != 1 || final[0].Kind != "final" || final[0].Ref != e.Item.Ref {
		t.Fatalf("final: %+v", final)
	}
	state.TurnStarted("child")
	e.Item.AsyncQuestion = true
	if updates := state.Observe(e, false); len(updates) != 0 {
		t.Fatalf("question entered text: %+v", updates)
	}
	if updates := state.TurnCompleted("child", "turn", "completed", e.At, true); len(updates) != 0 {
		t.Fatalf("question promoted: %+v", updates)
	}
	e.Item.AsyncQuestion, e.Item.Final = false, true
	state.Observe(e, false)
	if updates := state.TurnCompleted("child", "turn", "completed", e.At, true); len(updates) != 0 {
		t.Fatalf("explicit final promoted again: %+v", updates)
	}
}

func TestTranscriptHistoryAndAttachmentsAreIsolated(t *testing.T) {
	var service feed.Service
	var first, second scope.Store
	live := service.Transcript(&first)
	at := time.Unix(100, 0)
	ref := observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "reasoning"}
	e := observation.TextEvent{At: at, Kind: observation.ReasoningDelta, Delta: "Live", Item: observation.TextItem{Ref: ref, Type: "reasoning"}}
	live.Observe(e, false)
	history := []observation.TextEvent{{At: at, Kind: observation.ItemCompleted, Item: observation.TextItem{Ref: ref, Type: "reasoning", Summary: []string{"Old"}}}, {At: at, Kind: observation.ItemCompleted, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: "child", TurnID: "old", ItemID: "message"}, Type: "agentMessage", Text: "Old commentary"}}}
	older := transcript.History(history, false, false)
	if older["reasoning"].Live || older["message"].Kind != "text" {
		t.Fatalf("older page: %+v", older)
	}
	if newest := transcript.History(history, false, true); newest["message"].Kind != "final" {
		t.Fatalf("newest completed turn: %+v", newest)
	}
	e.Delta = " continuation"
	if got := live.Observe(e, false); len(got) != 1 || got[0].Text != "Live continuation" {
		t.Fatalf("history replaced live state: %+v", got)
	}
	if got := service.Transcript(&second).Observe(e, false); len(got) != 1 || got[0].Text != " continuation" {
		t.Fatalf("attachment inherited state: %+v", got)
	}
	first = scope.Store{}
	if got := service.Transcript(&first).Observe(e, false); len(got) != 1 || got[0].Text != " continuation" {
		t.Fatalf("reset inherited future text: %+v", got)
	}
}
