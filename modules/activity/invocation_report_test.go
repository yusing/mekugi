package activity_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestInvocationSegmentDetailLifecycle(t *testing.T) {
	service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, new(moduletest.Scripted))
	var store scope.Store
	start := time.Unix(100, 0)
	source := presentation.CommandSource{Script: "printf first; printf skipped; printf third"}
	invocation := presentation.NewInvocation(source, start, service.Outputs(&store))
	report := presentation.SegmentReport{Segments: []presentation.SegmentObservation{
		{Source: "printf first", Text: "Run printf first", Began: true, Started: start, Output: "first\n"},
		{Source: "printf skipped", Text: "Run printf skipped"},
		{Source: "printf third", Text: "Run printf third"},
	}}
	if changed, loss := invocation.ObserveSegments(report); !changed || loss {
		t.Fatalf("initial observation: changed=%v loss=%v", changed, loss)
	}
	before := invocation.ReportSnapshot(false)
	if !slices.Equal(before.Segments[0].Tail, []string{"first"}) {
		t.Fatalf("source summary waited for terminal reveal: %+v", before)
	}
	output := invocation.SegmentOutput(0)
	firstOutput := output.View()
	report.Segments[0].Output = "second\n"
	if changed, loss := invocation.ObserveSegments(report); changed || loss {
		t.Fatalf("output alone changed lifecycle: changed=%v loss=%v", changed, loss)
	}
	if got := output.View(); !slices.Equal(got.Lines, []string{"first", "second"}) || !slices.Equal(firstOutput.Lines, []string{"first"}) {
		t.Fatalf("drained output or earlier snapshot changed: before=%+v after=%+v", firstOutput, got)
	}
	report.Segments[0].Output = ""
	report.Segments[0].Finished, report.Segments[0].Ended = true, start.Add(time.Second)
	report.Segments[2].Began, report.Segments[2].Output = true, "third\n"
	if changed, loss := invocation.ObserveSegments(report); !changed || loss {
		t.Fatalf("segment transition: changed=%v loss=%v", changed, loss)
	}
	report.Segments[2].Output = ""
	report.Ended, report.Closed, report.Done, report.ExitCode = true, true, true, 7
	invocation.ObserveSegments(report)
	if invocation.ReportMatchesHost() {
		t.Fatal("report established host completion")
	}
	invocation.Complete(presentation.CommandCompletion{Source: source, At: start.Add(2 * time.Second), Aggregate: new("host aggregate"), Exit: new(7)})
	final := invocation.ReportSnapshot(true)
	if !slices.Equal(final.Segments[0].Tail, []string{"first", "second"}) || !slices.Equal(before.Segments[0].Tail, []string{"first"}) {
		t.Fatal("source summary lost drained bytes or mutated an earlier publication")
	}
	if !invocation.ReportMatchesHost() || !final.SeparateOutput || len(final.Segments) != 3 {
		t.Fatalf("completed attribution: %+v", final)
	}
	if len(before.Segments) != 3 || !before.Segments[0].Running || before.Segments[0].Output.Done || !before.Segments[1].Pending || !before.Segments[2].Pending {
		t.Fatalf("later completion mutated earlier publication: %+v", before)
	}
	command := invocation.Snapshot()
	command.Source = 42
	for _, segment := range final.Segments {
		command.Segments = append(command.Segments, segment.CommandSegment)
	}
	pages := command.DetailValues()
	if len(pages) != 3 || pages[0].Output != 2 || !pages[1].Skipped || pages[1].Output != 0 || pages[2].Output != 4 || !pages[2].Exited || pages[2].ExitCode != 7 || pages[2].Source != 42 {
		t.Fatalf("detail targets lost observed outcomes: %+v", pages)
	}
	if got := invocation.SegmentOutput(2).View(); !got.Done || !got.Exited || got.Exit != 7 || !slices.Equal(got.Lines, []string{"third"}) {
		t.Fatalf("shell exit did not settle its active segment: %+v", got)
	}
	if !invocation.ReleaseSegmentOutputs() || !output.View().Released || invocation.ReleaseSegmentOutputs() {
		t.Fatal("replacement did not release segment retention exactly once")
	}
}

func TestInvocationSegmentAttributionLoss(t *testing.T) {
	for _, cause := range []string{"terminal", "transport loss", "incomplete", "VCS bound", "unretained bound"} {
		t.Run(cause, func(t *testing.T) {
			service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, new(moduletest.Scripted))
			var store scope.Store
			retention := service.Outputs(&store)
			if cause == "unretained bound" {
				retention = nil
			}
			invocation := presentation.NewInvocation(presentation.CommandSource{}, time.Time{}, retention)
			report := presentation.SegmentReport{Segments: []presentation.SegmentObservation{
				{Source: "git status --short", Analysis: "git status --short", Began: true, Output: " M file\n"},
				{Source: "printf done"},
			}}
			if retention == nil {
				report.Segments[0].Analysis = "printf unretained"
			}
			invocation.ObserveSegments(report)
			output := invocation.SegmentOutput(0)
			report.Segments[0].Output = ""
			switch cause {
			case "terminal":
				report.Terminal = true
			case "transport loss":
				report.Lossy = true
			case "incomplete":
				report.Ended = true
			case "VCS bound", "unretained bound":
				report.Segments[0].Output = strings.Repeat("x", feed.OutputBytes)
			}
			changed, loss := invocation.ObserveSegments(report)
			if !changed || loss != strings.HasSuffix(cause, "bound") || invocation.ReportSnapshot(false).SeparateOutput || invocation.SegmentOutput(0) != nil {
				t.Fatalf("attribution survived %s: changed=%v loss=%v", cause, changed, loss)
			}
			if output != nil && !output.View().Released {
				t.Fatal("unattributable output remained retained")
			}
			report.Segments[0].Output = ""
			if _, repeated := invocation.ObserveSegments(report); repeated {
				t.Fatal("attribution loss repeatedly requested transport release")
			}
		})
	}
}

func TestInvocationReportRequiresMatchingKnownHostExit(t *testing.T) {
	for _, outcome := range []string{"matching", "unknown", "mismatch"} {
		t.Run(outcome, func(t *testing.T) {
			service := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, new(moduletest.Scripted))
			var store scope.Store
			invocation := presentation.NewInvocation(presentation.CommandSource{}, time.Time{}, service.Outputs(&store))
			report := presentation.SegmentReport{Ended: true, Closed: true, Done: true, Segments: []presentation.SegmentObservation{
				{Source: "git status --short", Analysis: "git status --short", Began: true, Finished: true, Output: " M file\n"},
				{Source: "printf done"},
			}}
			invocation.ObserveSegments(report)
			exit := new(0)
			if outcome == "unknown" {
				exit = nil
			} else if outcome == "mismatch" {
				*exit = 7
			}
			invocation.Complete(presentation.CommandCompletion{Exit: exit})
			if invocation.ReportMatchesHost() != (outcome == "matching") {
				t.Fatal("segment report replaced the authoritative host outcome")
			}
			if got := invocation.ReportSnapshot(true); got.Segments[0].Raw != " M file\n" || !got.Segments[1].Skipped {
				t.Fatalf("final report lost exact VCS bytes or skipped source: %+v", got)
			}
		})
	}
}

func TestInvocationCombinedOutputDoesNotAccumulateUnretainedBytes(t *testing.T) {
	invocation := presentation.NewInvocation(presentation.CommandSource{}, time.Time{}, nil)
	report := presentation.SegmentReport{Segments: []presentation.SegmentObservation{{
		Source: "printf combined", Began: true, Output: strings.Repeat("x", feed.OutputBytes),
	}}}
	for range 2 {
		if _, loss := invocation.ObserveSegments(report); loss || invocation.ReportSnapshot(false).SeparateOutput {
			t.Fatal("combined output accumulated an unused per-segment buffer")
		}
	}
}
