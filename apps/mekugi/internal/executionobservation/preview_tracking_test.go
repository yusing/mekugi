package executionobservation

import (
	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestExecPreviewTrackIdentityAndLifecycle(t *testing.T) {
	t.Parallel()
	const script = "sed -i 's/a/b/' a; go test ./...; sed -i 's/b/c/' a"
	for _, tc := range []struct {
		name                                                                 string
		thread, turn                                                         string
		ambiguous, pending, ended, done, lastEnded, wantTracked, wantSettled bool
	}{
		{name: "later edit pending", thread: "main", turn: "turn", wantTracked: true},
		{name: "last edit ended", thread: "main", turn: "turn", lastEnded: true, wantTracked: true, wantSettled: true},
		{name: "short circuited", thread: "main", turn: "turn", done: true, wantTracked: true, wantSettled: true},
		{name: "report lost", thread: "main", turn: "turn", ended: true},
		{name: "other thread", thread: "child", turn: "turn"},
		{name: "other turn", thread: "main", turn: "old"},
		{name: "pending duplicate", thread: "main", turn: "turn", pending: true, lastEnded: true, wantTracked: true},
		{name: "ambiguous", thread: "main", turn: "turn", ambiguous: true, lastEnded: true, wantTracked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
			tracking := newExecPreviewTrack(hub.Observer, tc.thread, tc.turn, []commandanalysis.Command{{Command: script}})
			defer tracking.Close()
			key := [3]string{"main", "turn", "one"}
			report := hub.begin(t, key, script)
			report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
			if tc.lastEnded {
				report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 2}, segmenttransport.Message{Type: segmenttransport.End, Index: 2, Code: new(0)})
			}
			if tc.done {
				report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
			}
			if tc.ended {
				report.conn.Close()
			}
			hub.await(t, key, func(view segments.Report) bool {
				return view.LastStarted() >= 1 && (!tc.lastEnded || view.LastStarted() == 2) && (!tc.done || view.Complete()) && (!tc.ended || view.Ended)
			})
			if tc.pending || tc.ambiguous {
				hub.start([3]string{"main", "turn", "two"}, "bash -lc "+quoteShellWord(script))
			}
			if tc.ambiguous {
				dialExecTrackReport(t, hub, script)
			}
			state := tracking.state()
			tracked, settled := state.tracked, state.settled
			if tracked != tc.wantTracked || settled != tc.wantSettled {
				t.Fatalf("state = %v, %v", tracked, settled)
			}
		})
	}
}

func TestSkippedEditSettlesBeforeUnrelatedCommand(t *testing.T) {
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	script := "false && sed -i 's/a/b/' skipped; go test ./..."
	tracking := newExecPreviewTrack(hub.Observer, "main", "turn", []commandanalysis.Command{{Command: script}})
	defer tracking.Close()
	report := hub.begin(t, [3]string{"main", "turn", "edit"}, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(1)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 2})
	hub.await(t, [3]string{"main", "turn", "edit"}, func(view segments.Report) bool { return view.LastStarted() == 2 })
	if state := tracking.state(); !state.tracked || !state.settled {
		t.Fatalf("skipped edit kept preview live: %+v", state)
	}
}
