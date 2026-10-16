package activity_test

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestServiceAttachmentOutputs(t *testing.T) {
	if activity.Module.Lifetime != module.AttachedSession {
		t.Fatal("activity must follow the attached session lifetime")
	}
	service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, nil)
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
