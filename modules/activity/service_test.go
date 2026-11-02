package activity_test

import (
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

type serviceSource struct{ presentation.Facts }

func (s serviceSource) ActivityFacts() presentation.Facts { return s.Facts }

type serviceAgent struct{ presentation.AgentFacts }

func (a serviceAgent) ActivityAgent() presentation.AgentFacts { return a.AgentFacts }

func TestServiceAttachmentOutputs(t *testing.T) {
	if activity.Module.Lifetime != module.AttachedSession {
		t.Fatal("activity must follow the attached session lifetime")
	}
	service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, new(moduletest.Scripted))
	var mainAttachment, separateAttachment scope.Store
	mainOutputs := service.Outputs(&mainAttachment)
	mainOutput := mainOutputs.New()
	mainOutput.Write("main output\n")
	mainOutput.Finish(nil, nil)
	childOutputs := service.Outputs(&mainAttachment)
	if childOutputs != mainOutputs {
		t.Fatal("Main and child consumers did not share attachment retention")
	}
	childOutput := childOutputs.New()
	childOutput.Write("child output\n")
	childOutput.Finish(nil, nil)
	separateOutputs := service.Outputs(&separateAttachment)
	if separateOutputs == mainOutputs {
		t.Fatal("distinct attachments shared retention")
	}
	separateOutput := separateOutputs.New()
	separateOutput.Write("separate output\n")
	separateOutput.Finish(nil, nil)
	for _, check := range []struct {
		output *feed.Output
		line   string
	}{
		{mainOutput, "main output"},
		{childOutput, "child output"},
		{separateOutput, "separate output"},
	} {
		if got := check.output.View(); !got.Done || !slices.Equal(got.Lines, []string{check.line}) {
			t.Errorf("retained output: got %+v, want completed %q", got, check.line)
		}
	}
	attached := presentation.Attached[serviceSource, serviceAgent](&mainAttachment)
	var mainThread, childThread scope.Store
	live := attached.Lifecycle(&mainThread)
	live.TurnStarted(time.Unix(100, 0))
	snapshot := *live
	attached.Main.Agents = []serviceAgent{{presentation.AgentFacts{Name: "/root", Responding: snapshot.Responding}}}
	live.TurnCompleted(true, time.Unix(105, 0))
	if !snapshot.Responding || snapshot.Final || attached.Main.Status(attached.Main.Agents[0]) != "responding" {
		t.Fatal("later lifecycle mutated a detached roster publication")
	}
	if same := presentation.Attached[serviceSource, serviceAgent](&mainAttachment); same != attached || same.Lifecycle(&mainThread) != live || !live.Final || live.Responding || live.WorkTimer.At(time.Unix(200, 0)) != 5*time.Second {
		t.Fatal("attachment replaced its authoritative thread lifecycle")
	}
	if child := attached.Lifecycle(&childThread); child == live || child.Turns != 0 {
		t.Fatal("child borrowed Main lifecycle")
	}
	if other := presentation.Attached[serviceSource, serviceAgent](&separateAttachment); other == attached || other.Lifecycle(&mainThread).Turns != 0 || len(other.Main.Agents) != 0 {
		t.Fatal("separate attachment reused presentation state")
	}
}

func TestAbsentServiceDoesNotRetainOutputs(t *testing.T) {
	var service *feed.Service
	// Absence requires neither an attachment store nor a retention allocation.
	if got := service.Outputs(nil); got != nil {
		t.Fatal("absent activity allocated retention")
	}
	var store scope.Store
	if got := service.Outputs(&store); got != nil {
		t.Fatal("an attachment store enabled absent activity")
	}
}
