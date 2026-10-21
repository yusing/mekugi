package activity_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/activitydetail"
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
	if got := service.ActivityDetail(&mainAttachment, activitydetail.Request{}); len(got.Pages) != 0 || got.Selected != "" {
		t.Fatalf("zero-provider host returned detail: %+v", got)
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

func TestServiceValidatesActivityDetailProviders(t *testing.T) {
	resolve := func(*scope.Store, activitydetail.Request) activitydetail.Result { return activitydetail.Result{} }
	for _, tt := range []struct {
		name      string
		providers []activitydetail.Provider
	}{
		{"empty name", []activitydetail.Provider{{Resolve: resolve}}},
		{"missing resolver", []activitydetail.Provider{{Name: "missing"}}},
		{"duplicate", []activitydetail.Provider{{Name: "same", Resolve: resolve}, {Name: "same", Resolve: resolve}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if service, err := activity.Module.New(activity.Deps{}, module.Slots{ActivityDetails: tt.providers}); err == nil || service != nil {
				t.Fatalf("invalid providers constructed service=%v err=%v", service, err)
			}
		})
	}
}

func TestServiceActivityDetailProviderOrder(t *testing.T) {
	if !activity.Module.HostsActivityDetails {
		t.Fatal("activity does not declare its detail host")
	}
	var store scope.Store
	request := activitydetail.Request{Thread: "thread", Item: "item", Path: "src", Directory: true}
	want := activitydetail.Result{Pages: []activitydetail.Page{{ID: "chosen", Code: "captured"}}, Selected: "chosen"}
	var calls []string
	var providers []activitydetail.Provider
	for _, name := range []string{"empty", "chosen", "later"} {
		providers = append(providers, activitydetail.Provider{Name: name, Resolve: func(gotStore *scope.Store, gotRequest activitydetail.Request) activitydetail.Result {
			calls = append(calls, name)
			if gotStore != &store || gotRequest != request {
				t.Fatal("provider lost the caller's store or request")
			}
			if name == "empty" {
				return activitydetail.Result{}
			}
			return want
		}})
	}
	service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{ActivityDetails: providers}, new(moduletest.Scripted))
	if got := service.ActivityDetail(&store, request); !reflect.DeepEqual(got, want) || !slices.Equal(calls, []string{"empty", "chosen"}) {
		t.Fatalf("provider resolution: result=%+v calls=%v", got, calls)
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
