package presentation

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestInvocationCompletionAndDetachedSnapshot(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "without retention", true: "with retention"}[retained], func(t *testing.T) {
			var outputs *feed.Retention
			if retained {
				outputs = new(feed.Retention)
			}
			start := time.Unix(100, 0)
			source := CommandSource{Script: "printf result", Display: "printf result", Directory: "/workspace"}
			invocation := NewInvocation(source, start, outputs)
			invocation.Write("loading\rresult\n")
			before := invocation.Snapshot()
			exit := 0
			invocation.Complete(CommandCompletion{Source: source, At: start.Add(time.Second), Duration: time.Second, Aggregate: new(""), Exit: &exit})
			exit = 7
			after := invocation.Snapshot()
			if invocation.Running() || after.Running || !after.Exited || after.ExitCode != 0 || after.Duration != time.Second || after.Started != start || after.Ended != start.Add(time.Second) || after.Script != source.Script {
				t.Fatalf("completion facts = %+v", after)
			}
			if !before.Running || before.Exited || !before.Ended.IsZero() {
				t.Fatalf("earlier publication changed: %+v", before)
			}
			if retained && (before.Output.Done || !after.Output.Done || after.OutputContent) || !retained && (before.Output != nil || after.Output != nil) {
				t.Fatalf("completion lost explicit empty output or mutated a snapshot: before=%+v after=%+v", before.Output, after.Output)
			}
			version := 0
			if retained {
				version = invocation.Output().Version()
			}
			invocation.Write("late output")
			invocation.Complete(CommandCompletion{Source: CommandSource{Script: "replacement"}, Exit: new(9)})
			late := invocation.Snapshot()
			if late.Script != after.Script || late.ExitCode != after.ExitCode || late.Ended != after.Ended || retained && invocation.Output().Version() != version {
				t.Fatalf("late observation replaced completion: %+v", late)
			}
		})
	}
}

func TestInvocationHistoricalUnknownOutcome(t *testing.T) {
	source := CommandSource{Script: "retained command"}
	invocation := NewInvocation(source, time.Time{}, new(feed.Retention))
	invocation.Complete(CommandCompletion{Source: source, Duration: time.Second, Aggregate: new("retained result")})
	view := invocation.Snapshot()
	if view.Running || view.Exited || !view.Started.IsZero() || !view.Ended.IsZero() || view.Duration != time.Second || !view.OutputContent || !view.Output.Done || view.Output.Exited {
		t.Fatalf("history invented a live clock or known exit: %+v", view)
	}
}
