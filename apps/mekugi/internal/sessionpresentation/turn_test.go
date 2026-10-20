package sessionpresentation

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func TestTurnPresentationKeepsSelectedTurnAndRetiresOnlyCompletedScope(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			var attached Attachment
			var text transcript.State
			var waits activity.Waits
			var agent activity.AgentLifecycle
			owners := TurnModels{Transcript: &text, Waits: &waits, Agent: &agent}
			at := time.Unix(100, 0)
			old := attached.Transcript(thread, "old")
			waits.Observe(old.Turn, activity.WaitObservation{Item: observation.Item{ID: "wait", Tool: "wait"}, Phase: "item/started", Observed: true})
			pending := text.ProviderStarted(thread, at, old.Thread)
			start := attached.ObserveTurn(owners, TurnObservation{Kind: observation.TurnStarted, Thread: thread, Turn: "current", At: at})
			owners.Selected = "current"
			end := TurnObservation{Kind: observation.TurnCompleted, Thread: thread, Turn: "old", Status: "completed", At: at.Add(time.Second)}
			updates := attached.ObserveTurn(owners, end)
			if !start.Admitted || !updates.Admitted || updates.Current || len(updates.Text) != 0 || len(updates.Waits) != 1 || !agent.Responding || attached.Transcript(thread, "old").Turn != nil {
				t.Fatalf("stale completion changed current presentation: start=%+v end=%+v agent=%+v", start, updates, agent)
			}
			if late := attached.ObserveTurn(owners, TurnObservation{Kind: observation.TurnStarted, Thread: thread, Turn: "old", At: end.At}); late.Admitted || agent.Turns != 1 {
				t.Fatalf("closed start revived a turn: %+v, agent=%+v", late, agent)
			}
			end.Turn = "current"
			updates = attached.ObserveTurn(owners, end)
			if !updates.Current || len(updates.Text) != 1 || updates.Text[0].Ref != pending[0].Ref || updates.Text[0].Phase != "discarded" || agent.Responding || !agent.Final {
				t.Fatalf("current completion did not settle its presentation: %+v, agent=%+v", updates, agent)
			}
		})
	}
}

func TestTurnPresentationCompletesResumedMainWithoutStart(t *testing.T) {
	var attached Attachment
	var text transcript.State
	var waits activity.Waits
	var agent activity.AgentLifecycle
	at := time.Unix(100, 0)
	// Resume selects an active host turn before deferred history. Its attachment
	// selection is borrowed; no observed start or host operation is invented.
	owners := TurnModels{Transcript: &text, Waits: &waits, Agent: &agent, Selected: "resumed"}
	end := TurnObservation{Kind: observation.TurnCompleted, Thread: "main", Turn: "older", Status: "completed", At: at.Add(2 * time.Second)}
	attached.ObserveTurn(owners, end)
	if agent.Final {
		t.Fatal("historical completion settled resumed Main")
	}
	end.Turn = "resumed"
	updates := attached.ObserveTurn(owners, end)
	if !updates.Current || !agent.Final || agent.Turns != 0 || !agent.Started.IsZero() {
		t.Fatalf("resume completion lost original admission/timing: %+v, %+v", updates, agent)
	}
	owners.Selected = ""
	end.Turn, end.Status = "without-start", "interrupted"
	updates = attached.ObserveTurn(owners, end)
	if !updates.Current || agent.Final {
		t.Fatal("unselected completion did not settle Activity without inventing a selected turn")
	}
}
