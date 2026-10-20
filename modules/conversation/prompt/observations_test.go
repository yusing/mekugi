package prompt

import (
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestMessageQuestionAdmissionAndSupersession(t *testing.T) {
	var questions Questions
	call := &Call{ID: "q", Thread: "main", Turn: "old", Item: "question", Questions: []Question{{Question: "Where?"}}}
	in := Message{Ref: observation.ItemRef{ThreadID: "main", TurnID: "old", ItemID: "question"}, Kind: observation.ItemCompleted, Call: call, History: true}
	if got := questions.ObserveMessage(in); !got.Suppress || got.Added != nil {
		t.Fatal("child question acquired Main's prompt state")
	}
	in.Main = true
	if got := questions.ObserveMessage(in); got.Added != call || !got.Suppress || questions.AutoOpen {
		t.Fatal("history did not admit a closed prompt")
	}
	in.Call, in.Kind = nil, observation.AgentDelta
	if got := questions.ObserveMessage(in); !got.Suppress {
		t.Fatal("question delta entered the transcript")
	}
	questions.Open(call)
	in = Message{Ref: observation.ItemRef{ThreadID: "main", TurnID: "next"}, Kind: observation.ItemCompleted, Main: true, User: true, HasReplies: true}
	if questions.NeedsParking(in) || len(questions.ObserveMessage(in).Changed) != 0 {
		t.Fatal("invalid reply envelope superseded a pending question")
	}
	in.HasReplies = false
	if questions.NeedsParking(in) || len(questions.ObserveMessage(in).Changed) != 0 {
		t.Fatal("later input in the same turn superseded a pending question")
	}
	in.Ref.TurnID = "new"
	if !questions.NeedsParking(in) {
		t.Fatal("supersession did not require parking")
	}
	questions.Hide()
	got := questions.ObserveMessage(in)
	if len(got.Changed) != 1 || !got.Changed[0].Superseded || !call.Resolved || call.Questions[0].Outcome != "superseded" {
		t.Fatalf("new ordinary input did not supersede the call: %+v", got)
	}
}
