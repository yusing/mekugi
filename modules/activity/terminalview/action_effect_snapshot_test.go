package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotCompoundCommandLifecycle(t *testing.T) {
	start := time.Unix(100, 0)
	source := presentation.CommandSource{Script: "a && b && c && d", Display: "a && b && c && d"}
	invocation := presentation.NewInvocation(source, start, new(feed.Retention))
	report := presentation.SegmentReport{}
	for _, command := range []string{"a", "b", "c", "d"} {
		report.Segments = append(report.Segments, presentation.SegmentObservation{Source: command, Text: presentation.RunSource(command)})
	}
	for phase := range 3 {
		name := []string{"first", "second", "failed"}[phase]
		report.Segments[0].Began, report.Segments[0].Started = true, start
		if phase > 0 {
			report.Segments[0].Finished = true
			report.Segments[1].Began, report.Segments[1].Started = true, start.Add(time.Second)
		}
		if phase == 0 {
			report.Segments[0].Output = "observed output\n"
		} else {
			report.Segments[0].Output = ""
		}
		if phase == 2 {
			report.Segments[1].Finished, report.Segments[1].ExitCode = true, 2
			report.Ended, report.Closed, report.Done, report.ExitCode = true, true, true, 2
			invocation.Complete(presentation.CommandCompletion{Source: source, Exit: new(2)})
		}
		invocation.ObserveSegments(report)
		command := invocation.Snapshot()
		command.Source = 7
		for _, segment := range invocation.ReportSnapshot(phase == 2).Segments {
			command.Segments = append(command.Segments, segment.CommandSegment)
		}
		values := (presentation.ItemContent{Seq: 7, Kind: "tool", Command: command, Details: &presentation.ItemDetails{CommandText: source.Script}}).Values()
		if len(values) != 4 || values[2].Pending != (phase < 2) || values[2].Skipped != (phase == 2) || values[2].Exited || !values[2].Started.IsZero() || len(values[0].Tail) == 0 {
			t.Fatalf("unreached segment lifecycle: %+v", values)
		}
		pages := presentation.RefreshDetails(presentation.DetailPages(values[:1]), map[uint64]presentation.DetailSource{7: {Entry: presentation.Entry{Values: values}, Command: &command}})
		if len(pages) != 4 || pages[2].Pending != (phase < 2) || pages[2].Running || pages[2].Output != 0 || pages[0].Output != 2 {
			t.Fatalf("pending detail lost its segment identity or borrowed output: %+v", pages)
		}
		p := Painter{Theme: diffrender.DarkTheme, Clock: func() time.Time { return start }}
		var rows []string
		for _, value := range values {
			rows = append(rows, p.Block(Block{Value: value}, 60)...)
		}
		uisnapshot.AssertTerminal(t, "testdata/snapshots/compound_lifecycle_"+name+".txt", append(rows, "plain"), 60)
	}
}

func TestUISnapshotCapturedEffectsUnderAction(t *testing.T) {
	for _, user := range []bool{false, true} {
		for _, restored := range []bool{false, true} {
			name := fmt.Sprintf("user_%t_restored_%t", user, restored)
			t.Run(name, func(t *testing.T) {
				command := presentation.Command{Source: 1, UserShell: user, Script: "rm old.go; false", Display: "rm old.go; false", Captured: true,
					CaptureText: "Delete `old.go` · +0 −3 · rm", Exited: true, ExitCode: 1, Duration: time.Second, Tail: []string{"failure detail"}}
				item := presentation.ItemContent{Seq: 1, Kind: "tool", Command: command, Details: &presentation.ItemDetails{CommandText: command.Script}}
				values := item.Values()
				values = presentation.CommandExit(values, command.Display, 1, command.Tail, 0, false)
				if len(values) != 2 || !values[1].Effect || values[1].Exited || values[1].ExitCode != 0 || values[0].Operation == values[1].Operation {
					t.Fatalf("effect borrowed action outcome or identity: %+v", values)
				}
				v := NewView()
				v.Conversation = true
				v.Painter.Theme = diffrender.DarkTheme
				v.Update(&presentation.Publication{Entries: []presentation.Entry{{Facts: presentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: command.Script, Restored: restored}, Values: values}}}, Display{Caller: "/root"})
				rendered := v.RenderConversation(70)
				plain := ansi.Strip(strings.Join(rendered.Lines, "\n"))
				if strings.Count(plain, "Deleted") != 1 || !strings.Contains(plain, "exit 1") || !strings.Contains(plain, "failure detail") {
					t.Fatalf("action/effect rendering: %s", plain)
				}
				if target := rendered.Snippets[len(rendered.Snippets)-1]; target.Run != 1 || target.Block != EditNavigationSnippet || target.Path != "old.go" {
					t.Fatalf("effect lost retained-change navigation: %+v", target)
				}
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/action_effect_user_%t.txt", user), append(rendered.Lines, "plain"), 70)
			})
		}
	}
}

func TestUISnapshotSilentCapturedWriterTiming(t *testing.T) {
	start := time.Unix(100, 0)
	source := presentation.CommandSource{Script: "cat > file; go test ./...", Display: "cat > file; go test ./..."}
	invocation := presentation.NewInvocation(source, start, new(feed.Retention))
	invocation.ObserveSegments(presentation.SegmentReport{Ended: true, Closed: true, Done: true, Segments: []presentation.SegmentObservation{
		{Source: "cat > file", Text: presentation.ShellSource("cat > file", ""), Began: true, Finished: true, Started: start, Ended: start.Add(2 * time.Second), Duration: 2 * time.Second},
		{Source: "go test ./...", Text: presentation.ShellSource("go test ./...", ""), Began: true, Finished: true, Started: start.Add(2 * time.Second), Ended: start.Add(3 * time.Second), Duration: time.Second},
	}})
	invocation.Complete(presentation.CommandCompletion{Source: source, Duration: 3 * time.Second, Aggregate: new(""), Exit: new(0)})
	command := invocation.Snapshot()
	command.Captured, command.CaptureText = true, "Edit `file` · +1 −0 · cat"
	for _, segment := range invocation.ReportSnapshot(true).Segments {
		command.Segments = append(command.Segments, segment.CommandSegment)
	}
	values := (presentation.ItemContent{Seq: 9, Kind: "tool", Command: command, Details: &presentation.ItemDetails{CommandText: source.Script}}).Values()
	if len(values) != 3 || !values[0].Segment || values[0].Duration != 2*time.Second || values[0].Output != 2 || values[1].Duration != time.Second || values[2].Effect || values[2].BatchExit || values[2].Duration != 0 || values[2].Source != 9 || values[2].EditPath() != "file" {
		t.Fatalf("silent writer lost individual timing or effect borrowed aggregate time: %+v", values)
	}
	p := Painter{Theme: diffrender.DarkTheme}
	var rows []string
	for _, value := range values {
		rows = append(rows, p.Block(Block{Value: value}, 70)...)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/silent_captured_writer_timing.txt", append(rows, "plain"), 70)
}

func TestUISnapshotCapturedSetupKeepsObservedAction(t *testing.T) {
	for _, tc := range []struct {
		name, script, output string
		exit                 int
	}{
		{"success", "set -e; sed -i s/old/kept/ file; cat file", "kept\n", 0},
		{"failure", "set -e; sed -i s/old/kept/ file; cat missing", "cat: missing: No such file or directory\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, ok := shellinstrumentation.Split(tc.script)
			if !ok || len(parts) != 1 || len(presentation.Operations(presentation.ShellSource(parts[0].Source, ""))) < 2 {
				t.Fatal("fixture must classify several operations inside one observed segment")
			}
			start := time.Unix(100, 0)
			source := presentation.CommandSource{Script: tc.script, Display: tc.script}
			invocation := presentation.NewInvocation(source, start, new(feed.Retention))
			invocation.ObserveSegments(presentation.SegmentReport{Ended: true, Closed: true, Done: true, ExitCode: tc.exit, Segments: []presentation.SegmentObservation{{
				Source: parts[0].Source, Text: presentation.ShellSource(parts[0].Source, ""), Began: true, Finished: true, ExitCode: tc.exit,
				Started: start, Ended: start.Add(time.Second), Duration: time.Second, Output: tc.output,
			}}})
			invocation.Complete(presentation.CommandCompletion{Source: source, Duration: time.Second, Aggregate: new(tc.output), Exit: new(tc.exit)})
			command := invocation.Snapshot()
			command.Source, command.Captured, command.CaptureText = 9, true, "Edit `file` · +1 −1 · bash"
			command.Approval = "Approved"
			command.Tail = invocation.Output().View().Lines
			for _, part := range invocation.ReportSnapshot(true).Segments {
				segment := part.CommandSegment
				segment.Writer = tc.exit == 0
				command.Segments = append(command.Segments, segment)
			}
			values := (presentation.ItemContent{Seq: 9, Kind: "tool", Command: command, Details: &presentation.ItemDetails{CommandText: tc.script}}).Values()
			if len(values) != 2 || !values[0].Segment || values[0].Code != tc.script || values[0].Output != 1 || values[0].ExitCode != tc.exit || values[0].Duration != time.Second || values[0].BatchExit || values[0].Approval != "Approved" || !values[1].Effect || values[1].Exited {
				t.Fatalf("single observed action lost source, output or effect membership: %+v", values)
			}
			pages := command.DetailValues()
			if len(pages) != 1 || pages[0].Code != tc.script || pages[0].Output != 1 || strings.Join(pages[0].Tail, "\n") != strings.TrimRight(tc.output, "\n") {
				t.Fatalf("details invented per-operation boundaries: %+v", pages)
			}
			p := Painter{Theme: diffrender.DarkTheme}
			var rows []string
			for _, value := range values {
				rows = append(rows, p.Block(Block{Value: value}, 90)...)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/captured_setup_"+tc.name+".txt", append(rows, "plain"), 90)
		})
	}
}
