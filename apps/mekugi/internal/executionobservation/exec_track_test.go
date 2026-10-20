package executionobservation

import (
	"github.com/yusing/mekugi/libraries/commandanalysis"

	"testing"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestExecWindowRepeatedOpenKeepsOneReportOwner(t *testing.T) {
	t.Parallel()
	server, err := segmenttransport.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	observer, err := segments.NewObserver(server, segments.Instrumentation{}, activitymodel.OutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	registry := &Windows{Reports: observer}
	command := commandanalysis.Command{Command: "mktemp", Workdir: t.TempDir(), Shell: "bash"}
	first := &execWindow{ref: "call", thread: "thread", turn: "turn"}
	registry.open(first, command)
	registry.open(&execWindow{ref: "call", thread: "thread", turn: "turn"}, command)
	if first.tracking == nil {
		t.Fatal("missing first subscription")
	}
	state := first.tracking.State()
	if len(state.Matches) != 1 || state.Matches[0].Ambiguous {
		t.Fatalf("repeated open changed report ownership: %+v", state)
	}

}
