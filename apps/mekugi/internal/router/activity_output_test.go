package router

import (
	"strings"
	"testing"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestOutputDemandsWithoutRenderer(t *testing.T) {
	output := new(activitymodel.Retention).New()
	output.Finish(new(strings.Repeat("retained output\n", 100)), new(0))
	v := &activityAdapter{Model: &activitypresentation.Model[activityPaneEntry, activityPaneAgent]{
		Entries: []liveActivityRecord{{Source: activityPaneEntry{Seq: 7, native: &liveActivityNativeItem{output: output}}}},
	}}
	demands := []activitypresentation.OutputDemand{
		{Source: 7, Output: 1, Version: output.Version(), Bytes: 100},
		{Source: 7, Output: 1, Version: output.Version()},
		{Source: 8, Output: 1},
	}
	results := v.resolveOutputDemands(demands)
	for i, result := range results {
		if result.Demand != demands[i] {
			t.Fatal("response lost source/version/bound identity")
		}
	}
	if !results[0].Limited || len(strings.Join(results[0].View.Lines, "\n")) > 100 || results[1].Limited || len(results[1].View.Lines) != 100 || !results[2].View.Released {
		t.Fatalf("bounded/full/missing responses: %+v", results)
	}
	output.Release()
	if len(results[1].View.Lines) != 100 || results[1].View.Released {
		t.Fatal("detached result changed after output release")
	}
}
