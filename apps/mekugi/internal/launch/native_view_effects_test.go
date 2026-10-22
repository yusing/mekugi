package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeDeliveryBatchPublicationAndOrdering(t *testing.T) {
	var effects nativeViewEffects
	deliver := func(text string) {
		effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Kind: sessionpresentation.CommandDelta, Delta: text}}})
	}
	deliver("first")
	deliver("second")
	first := effects.activityPublication().Effects[0]
	if len(effects.pending) != 0 || len(effects.activity) != 1 || len(first.Deliveries) != 2 {
		t.Fatal("unpublished deliveries were not batched")
	}
	deliver("third")
	if len(effects.activity) != 2 || len(first.Deliveries) != 2 || effects.activity[1].ID == first.ID {
		t.Fatal("later delivery extended an already-published effect")
	}
	if !effects.acknowledge(first.ID, false) || len(effects.activity) != 1 || effects.activity[0].Deliveries[0].Command.Delta != "third" {
		t.Fatal("earlier receipt consumed a later delivery")
	}
	effects.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "editor"}})
	deliver("fourth")
	if len(effects.pending) != 1 || len(effects.activity) != 2 || effects.pending[0].Restore == nil || effects.activity[1].Deliveries[0].Command.Delta != "fourth" || effects.activity[1].ID <= effects.pending[0].ID {
		t.Fatal("batching crossed an editor-effect boundary")
	}
}

func TestNativeDeliveryBatchBounds(t *testing.T) {
	for _, test := range []struct {
		name, text string
		count      int
	}{
		{"rows", "small", 65},
		{"bytes", strings.Repeat("x", 40<<10), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var effects nativeViewEffects
			for range test.count {
				effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Delta: test.text}}})
			}
			if len(effects.activity) != 2 || len(effects.pending) != 0 {
				t.Fatalf("batch bound lost: activity=%d semantic=%d", len(effects.activity), len(effects.pending))
			}
		})
	}
}

func TestNativeEffectReservationSurvivesActivityCompaction(t *testing.T) {
	var effects nativeViewEffects
	reservation := effects.reserve(nativeSemanticEffects)
	if reservation == nil || effects.reserve(1) != nil {
		t.Fatal("admission did not reserve finite consequence capacity")
	}
	for range 200 {
		effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Delta: "progress"}}})
		effects.activityPublication()
	}
	for range nativeSemanticEffects {
		reservation.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "retained"}})
	}
	reservation.release()
	if effects.reserved != 0 || len(effects.pending) != nativeSemanticEffects || effects.reset == 0 {
		t.Fatal("progress consumed reserved semantic capacity")
	}
	first := effects.pending[0].ID
	if effects.reserve(1) != nil || !effects.acknowledge(first, false) {
		t.Fatal("full retention did not wait for an actual receipt")
	}
	next := effects.reserve(1)
	if next == nil {
		t.Fatal("receipt did not free admission capacity")
	}
	next.release()
	if effects.reserved != 0 {
		t.Fatal("canceled admission retained capacity")
	}
}

func TestNativeOversizedActivityUsesPublicationCheckpoint(t *testing.T) {
	var effects nativeViewEffects
	effects.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "retained"}})
	id := effects.pending[0].ID
	effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Delta: strings.Repeat("x", 65<<10)}}})
	frame := effects.activityPublication()
	if frame.Reset <= id || len(frame.Effects) != 0 || len(effects.pending) != 1 || effects.pending[0].ID != id {
		t.Fatal("large reconstructible progress damaged semantic retention")
	}
}

func TestNativeActivityCatchupKeepsRenamedSelection(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	previous := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Thread: "first", Name: "/a"}}, {Agent: activity.Agent{Thread: "second", Name: "/b"}}}}
	event := c.event
	event.Frame.Agents = previous
	c.accept(event)
	c.renderer.terminal.Agents.Selected, c.renderer.terminal.Agents.RosterSelected = "/a", "/b"
	var effects nativeViewEffects
	for range 200 {
		effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Delta: "progress"}}})
		effects.activityPublication()
	}
	// Reusing the old label for another thread must not retarget selection when
	// metadata updates and a compacted progress window arrive together.
	effects.deliver("main", []sessionpresentation.ActivityDelivery{{Rename: &sessionpresentation.CallerRename{Old: "/a", Name: "/b"}}, {Rename: &sessionpresentation.CallerRename{Old: "/b", Name: "/c"}}})
	event.Frame.Agents = &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Thread: "first", Name: "/b"}}, {Agent: activity.Agent{Thread: "second", Name: "/c"}}}}
	event.Frame.Activity = effects.activityPublication()
	c.accept(event)
	if c.renderer.terminal.Agents.Selected != "/b" || c.renderer.terminal.Agents.RosterSelected != "/c" {
		t.Fatal("progress catchup or caller rename redirected stable thread selection")
	}
}
