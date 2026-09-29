package activity_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestThinkingBlock(t *testing.T) {
	var p activityui.Painter
	body := "one\n\ntwo\n\nthree\n\nfour\n\nfive"
	plain := func(block activityui.Block) []string {
		rows := p.Block(block, 40)
		for i, row := range rows {
			rows[i] = strings.TrimRight(ansi.Strip(row), " ")
		}
		return rows
	}
	// Streaming provider reasoning keeps its latest text rows under a header.
	if got := plain(activityui.Block{Kind: "summary", Body: body, Live: true}); strings.Join(got, "|") != "• Thinking… · +2 lines|  three|  four|  five" {
		t.Fatalf("live thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "one\n\ntwo", Live: true}); strings.Join(got, "|") != "• Thinking…|  one|  two" {
		t.Fatalf("short live thinking = %q", got)
	}
	// Finished thinking reports its time and keeps every row.
	got := plain(activityui.Block{Kind: "summary", Body: body, Elapsed: "12s"})
	if got[0] != "• Thought for 12s" || len(got) != 10 || got[9] != "  five" {
		t.Fatalf("finished thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: body, Elapsed: "12s", Collapsed: true}); strings.Join(got, "|") != "• Thought for 12s" {
		t.Fatalf("folded thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "one"}); strings.Join(got, "|") != "• Thought|  one" {
		t.Fatalf("untimed thinking = %q", got)
	}
	// Codex summaries keep their bullet body, even while streaming.
	if got := plain(activityui.Block{Kind: "summary", Body: "**Checking**\n\nPublic summary.", Live: true}); strings.Join(got, "|") != "• Public summary." {
		t.Fatalf("titled summary = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "**Check", Live: true}); strings.Contains(strings.Join(got, "|"), "Thinking") {
		t.Fatalf("partial title rendered as thinking = %q", got)
	}
}

func TestConsecutiveReasoningGroups(t *testing.T) {
	var p activityui.Painter
	summaries := []activityui.Block{
		{Kind: "summary", Body: "**First**\n\nEarlier body\nextra detail", Source: 1},
		{Kind: "summary", Body: "**Second**", Source: 2},
		{Kind: "summary", Body: "Latest body", Source: 3, Collapsed: true},
	}
	grouped := activityui.GroupReasoning(summaries)
	if len(grouped) != 2 || len(grouped[0].Members) != 2 || grouped[1].Source != 3 || grouped[1].Collapsed {
		t.Fatalf("group = %+v", grouped)
	}
	collapsed := ansi.Strip(strings.Join(p.Block(grouped[0], 80), "\n"))
	if collapsed != "• extra detail, Second" {
		t.Fatalf("collapsed = %q", collapsed)
	}
	grouped[0].Collapsed = false
	expanded := ansi.Strip(strings.Join(p.Block(grouped[0], 80), "\n"))
	if !strings.Contains(expanded, "Earlier body") || !strings.Contains(expanded, "Second") {
		t.Fatalf("expanded = %q", expanded)
	}
	if got := ansi.Strip(p.Summary(summaries)); got != "extra detail, Second, Latest body" {
		t.Fatalf("inline = %q", got)
	}
	separated := append(append([]activityui.Block{}, summaries[:1]...), activityui.Block{Kind: "op", Verb: "Read"})
	separated = append(separated, summaries[1:]...)
	if got := activityui.GroupReasoning(separated); len(got) != 4 || got[0].Collapsed || got[1].Kind != "op" {
		t.Fatalf("action boundary = %+v", got)
	}
}
