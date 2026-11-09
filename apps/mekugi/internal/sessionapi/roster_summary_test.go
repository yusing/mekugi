package sessionapi

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestFailedHookReachesRosterConsumers(t *testing.T) {
	source := &sessionpresentation.ActivitySource{Model: new(activity.Model[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent])}
	source.Apply(sessionpresentation.ActivityEvent{
		Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", AgentLifecycle: activity.AgentLifecycle{Responding: true}}},
		Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/worker", Kind: "hook", Text: "Stop · builtin",
			Native: &sessionpresentation.NativeItem{Phase: "item/completed", Hook: &activity.HookDetails{Status: "failed", HasError: true}}}},
	})
	publication := source.Publication(new(sessionpresentation.Model), nil)
	row := projectRoster(publication, "/root").Agents[0]
	if row.Activity == nil || len(row.Activity.Values) != 1 {
		t.Fatalf("hook summary absent: %+v", row.Activity)
	}
	summary := row.Activity.Values[0]
	canonical := source.Entries[0].Values[0]
	if summary.Verb != canonical.RowVerb() || !summary.Failed {
		t.Fatalf("hook outcome lost at API boundary: summary=%+v", summary)
	}
	painter := terminalview.Painter{}
	painted := ansi.Strip(painter.Summary([]terminalview.Block{{Value: canonical}}, 80))
	if !strings.Contains(painted, summary.Verb) || !strings.Contains(painted, summary.Label) {
		t.Fatalf("terminal and browser roster semantics diverged: %q / %+v", painted, summary)
	}
}

func TestRosterValuesRetainAllOperationOutcomes(t *testing.T) {
	item := activity.ItemContent{Kind: "tool", Text: activity.ShellSource("false; printf later", ""), Details: &activity.ItemDetails{CommandText: "false; printf later"}, Command: activity.Command{Script: "false; printf later", Display: "false; printf later", Segments: []activity.CommandSegment{
		{Source: "false", Text: activity.ShellSource("false", ""), ExitCode: 3},
		{Source: "printf later", Text: activity.ShellSource("printf later", ""), Skipped: true},
	}}}
	entry := activity.Entry{Facts: item.Facts, Values: item.Values()}
	publication := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Summary: []activity.Entry{entry}}}}}
	projected := projectRoster(publication, "/root").Agents[0].Activity
	if len(projected.Values) != len(entry.Values) || !projected.Values[0].Failed || !strings.Contains(projected.Values[0].Label, "exit 3") {
		t.Fatalf("multi-operation roster lost failure or skipped work: %+v", projected)
	}
	for i, summary := range projected.Values {
		if summary.Verb != entry.Values[i].RowVerb() {
			t.Fatalf("operation %d lost its shared observed verb: %+v", i, summary)
		}
	}
}

func TestRosterReasoningUsesLatestHeading(t *testing.T) {
	item := activity.ItemContent{Kind: "reasoning", Text: "# First\n\nChecking callers.\n\n# Second\n\nChecking outcomes."}
	entry := activity.Entry{Facts: item.Facts, Values: item.Values()}
	publication := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Summary: []activity.Entry{entry}}}}}
	values := projectRoster(publication, "/root").Agents[0].Activity.Values
	if len(values) != 1 || values[0].Label != entry.Values[1].ReasoningLabel() || values[0].Body != "" {
		t.Fatalf("latest reasoning heading lost or retained body exposed: %+v", values)
	}
}

func TestRosterSummaryBounds(t *testing.T) {
	program := "print('" + strings.Repeat("界", 300) + "')\nprint('later')"
	command := activity.ItemContent{Kind: "tool", Text: activity.ShellSource(program, ""), Details: &activity.ItemDetails{CommandText: program}, Command: activity.Command{Script: program, Display: program, Running: true}}
	reasoning := activity.ItemContent{Kind: "reasoning", Text: strings.Repeat("A long paragraph. ", 100), Details: &activity.ItemDetails{ReasoningLive: true}}
	for _, item := range []activity.ItemContent{command, reasoning} {
		entry := activity.Entry{Facts: item.Facts, Values: item.Values()}
		publication := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Summary: []activity.Entry{entry}}}}}
		projected := projectRoster(publication, "/root").Agents[0].Activity
		if len(projected.Values) == 0 {
			t.Fatal("missing compact activity")
		}
		for _, summary := range projected.Values {
			if len([]rune(summary.Code)) > 256 || strings.Contains(summary.Code, "\n") || summary.Body != "" || item.Kind == "reasoning" && summary.Label != "" || item.Kind == "tool" && summary.Code != string([]rune(program)[:256]) {
				t.Fatalf("roster copied long source/reasoning: %+v", summary)
			}
		}
	}
}

func TestRosterDiagnosticUsesBoundedSharedPreview(t *testing.T) {
	diagnostic := "error headline\n" + strings.Repeat("retained diagnostic details\n", 1000) + "last diagnostic"
	item := activity.ItemContent{Kind: "error", Text: activity.ErrorPreview(diagnostic), ErrorDetail: diagnostic}
	entry := activity.Entry{Facts: item.Facts, Values: item.Values()}
	publication := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{Summary: []activity.Entry{entry}}}}}
	projected := projectRoster(publication, "/root").Agents[0].Activity
	summary := projected.Values[0]
	if summary.Label != activity.ErrorPreview(diagnostic) || !summary.Failed {
		t.Fatalf("roster diagnostic lost the bounded preview: %+v", summary)
	}
	painter := terminalview.Painter{}
	block := terminalview.Block{Value: entry.Values[0]}
	if !strings.Contains(ansi.Strip(painter.Summary([]terminalview.Block{block}, 80)), summary.Label) || painter.DialogPage(block, 80).Text != diagnostic {
		t.Fatal("bounded roster preview changed the full retained diagnostic")
	}
}

func TestRosterReasoningReplacesAcrossUpdatesAndTurns(t *testing.T) {
	for _, restored := range []bool{false, true} {
		source := &sessionpresentation.ActivitySource{Model: new(activity.Model[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent]), Audience: sessionpresentation.ChildAudience}
		source.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", AgentLifecycle: activity.AgentLifecycle{Responding: true}}}})
		for i, step := range []struct{ item, turn, text, phase, want string }{
			{"one", "first", "**Inspecting**", "summary", "Inspecting"},
			{"one", "first", "**Inspecting**\n\nDetails.\n\n**Validating**", "summary", "Validating"},
			{"two", "first", "**Finishing**", "summary", "Finishing"},
			{"two", "first", "**Finishing**", "item/completed", "Finishing"},
			{"three", "fresh", "**Fresh work**", "summary", "Fresh work"},
			{"three", "fresh", "**Fresh work**", "item/completed", "Fresh work"},
			{"four", "fresh", strings.Repeat("An untitled paragraph. ", 10), "summary", "Thinking"},
			{"four", "fresh", strings.Repeat("An untitled paragraph. ", 10), "item/completed", "Thought"},
		} {
			source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: "/root/worker", Kind: "reasoning", CallID: step.item, Text: step.text,
				Native: &sessionpresentation.NativeItem{Thread: "worker", Turn: step.turn, Item: step.item, Phase: step.phase, Live: !restored}}}})
			publication := source.Publication(new(sessionpresentation.Model), nil)
			values := projectRoster(publication, "/root").Agents[0].Activity.Values
			if len(values) != 1 || values[0].Verb+values[0].Label != step.want || values[0].Body != "" {
				t.Fatalf("restored=%v step=%d: %+v", restored, i, values)
			}
			var blocks []terminalview.Block
			for _, entry := range publication.Roster[0].Agent.Summary {
				for _, value := range entry.Values {
					blocks = append(blocks, terminalview.Block{Value: value})
				}
			}
			painter := terminalview.Painter{}
			terminalWant := step.want
			if terminalWant == "Thinking" {
				terminalWant += "…"
			}
			if got := ansi.Strip(painter.Summary(blocks, 80)); got != terminalWant {
				t.Fatalf("terminal step=%d: %q", i, got)
			}
		}
		if len(source.Entries) != 4 || !strings.Contains(source.Entries[0].Source.Text, "Inspecting") {
			t.Fatal("roster replacement discarded transcript history")
		}
	}
}
