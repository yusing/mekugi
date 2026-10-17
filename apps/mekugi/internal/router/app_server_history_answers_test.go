package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestAppServerRestoredChildAnswerIgnoresTrailingWhitespace(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	u.restoring.root.Turns = []binding.HistoryTurn{{ID: "root-turn", Status: "completed", StartedAt: 100, CompletedAt: 110}}
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{
		historyItem("blank", " \n\t ", 109000),
		historyItem("answer", "Substantive answer", 105000),
	}})
	assertRestoredChildMainAnswer(t, u, "answer", "Substantive answer")
}

func TestAppServerRestoredChildExplicitFinalSurvivesCommentary(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	u.restoring.root.Turns = []binding.HistoryTurn{{ID: "root-turn", Status: "completed", StartedAt: 100, CompletedAt: 110}}
	historyTurnsReply(t, u)
	final := historyItem("answer", "Explicit final answer", 105000)
	final["item"].(map[string]any)["phase"] = "final_answer"
	restoreContentReply(t, u, 4, map[string]any{"data": []any{
		historyItem("commentary", "Later commentary", 109000),
		final,
	}})
	assertRestoredChildMainAnswer(t, u, "answer", "Explicit final answer")
	main := ansi.Strip(strings.Join(u.view.RenderFeed(100, 60).Lines, "\n"))
	u.agents.setDisplayFilter(false, "/root/worker")
	child := ansi.Strip(strings.Join(u.agents.RenderFeed(100, 60).Lines, "\n"))
	if strings.Contains(main, "Later commentary") || !strings.Contains(child, "Later commentary") {
		t.Fatalf("commentary was promoted to Main or lost from child history: main=%q child=%q", main, child)
	}
}

func assertRestoredChildMainAnswer(t *testing.T, u *appServerUI, item, text string) {
	t.Helper()
	for _, view := range []*activityAdapter{u.agents, u.view} {
		finals := 0
		for _, entry := range view.Entries {
			if entry.Source.Kind != "final" {
				continue
			}
			finals++
			n := entry.Source.Native
			if entry.Source.Text != text || n == nil || n.Thread != "child" || n.Turn != "t" || n.Item != item {
				t.Fatalf("restored final lost its answer or source identity: %+v", entry.Source)
			}
		}
		if finals != 1 {
			t.Fatalf("restored child answer produced %d finals, want one", finals)
		}
	}
	main := ansi.Strip(strings.Join(u.view.RenderFeed(100, 60).Lines, "\n"))
	if strings.Count(main, text) != 1 {
		t.Fatalf("Main did not render the child answer exactly once:\n%s", main)
	}
}
