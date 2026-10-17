package composition

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/usage"
)

func TestCompositionPresentationUsageSelection(t *testing.T) {
	for _, included := range []bool{false, true} {
		var c Composition
		history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
		if included {
			c.Add(usage.Module, func() usage.Deps { return usage.Deps{History: history.Value()} })
		}
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := runtime.Close(t.Context()); err != nil {
				t.Error(err)
			}
		})
		var store scope.Store
		model := runtime.Presentation.Session(&store)
		thread := presentation.Thread{ID: "child", OutputThroughput: inference.OutputThroughput{OutputTokens: 40, DurationNanos: uint64(time.Second), MeasuredRequests: 1}}
		for _, placement := range []presentation.Placement{presentation.ComposerCaption, presentation.AgentMetric} {
			items := model.StatusItems(thread, placement)
			if !included && len(items) != 0 || included && (len(items) != 1 || items[0].ID != "usage.throughput" || items[0].Thread != thread.ID || items[0].Text != "40.0 tok/s") {
				t.Fatalf("Usage=%v %s: %+v", included, placement, items)
			}
		}
	}
}

func TestCompositionRejectsDuplicatePresentationBeforeConstruction(t *testing.T) {
	var c Composition
	for _, id := range []module.ID{"one", "two"} {
		d := descriptor(id)
		d.Contributes.Presentation = usage.Module.Contributes.Presentation
		d.New = func(struct{}, module.Slots) (*testService, error) {
			t.Fatal("invalid composition constructed a module")
			return nil, nil
		}
		c.Add(d, emptyDeps)
	}
	if _, err := c.Activate(t.Context(), new(binding.Registry)); err == nil || !strings.Contains(err.Error(), "duplicate presentation") {
		t.Fatalf("duplicate contribution: %v", err)
	}
}
