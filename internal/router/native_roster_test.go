package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerChildMetadataWithoutThreadStarted(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	const child = "0123456789-child"
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": child, "turn": map[string]any{"id": "t"}})
	id := u.session.metadata[child]
	if id == "" || u.requests[id] != "thread/read" {
		t.Fatal("child observation did not request metadata")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": child, "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "exitCode": 2}})
	u.agents.selected = appServerPlaceholder(child)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "spawn", "type": "subAgentActivity", "kind": "started", "agentThreadId": child, "agentPath": "/root/checker"}})
	if u.session.metadata[child] != id || u.session.paths[child] != "/root/checker" || u.agents.selected != "/root/checker" {
		t.Fatal("activity identity or in-flight metadata request lost")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":%q,"agentNickname":"random-nickname","agentRole":"review","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"main","agent_path":"/root/checker","agent_role":"review"}}}}}}`, id, child))
	row := ansi.Strip(strings.Join(u.agents.nativeRoster(140, 6, time.Now(), true), "\n"))
	if !strings.Contains(row, "checker") || !strings.Contains(row, "● review") || !strings.Contains(row, "go test ./...") || strings.Contains(row, "01234567") {
		t.Fatalf("metadata roster: %s", row)
	}
	u.agents.only = true
	feed := ansi.Strip(strings.Join(u.agents.renderFeed(120, 30).lines, "\n"))
	if !strings.Contains(feed, "exit 2") || strings.Contains(feed, "01234567") {
		t.Fatalf("late rename lost retained activity: %s", feed)
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": child, "turn": map[string]any{"id": "t", "status": "completed"}})
	if len(u.requests) != 0 || u.session.metadata[child] != "" {
		t.Fatalf("metadata read repeated: %+v", u.requests)
	}
}

func TestAppServerChildMetadataFailureKeepsActivity(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t"}})
	id := u.session.metadata["child"]
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%s,"error":{"code":-1,"message":"not available"}}`, id))
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "mcat child.go"}})
	if len(u.requests) != 0 || u.alert || !strings.Contains(ansi.Strip(u.agents.agentState(*u.session.agent("/root/child"))), "child.go") {
		t.Fatal("metadata failure blocked activity or caused repeated reads")
	}
}

func TestNativeRosterShowsActivityDetails(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reviewer", "agentRole": "review"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "mcat " + thread + ".go"}})
	}
	got := ansi.Strip(strings.Join(u.agents.nativeRoster(140, 6, time.Now(), true), "\n"))
	for _, want := range []string{"main.go", "child.go", "reviewer", "● review", "Read"} {
		if !strings.Contains(got, want) {
			t.Fatalf("roster missing %q: %s", want, got)
		}
	}
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "reasoning", "delta": "Checking event routing"})
	got = ansi.Strip(strings.Join(u.agents.nativeRoster(140, 6, time.Now(), true), "\n"))
	if !strings.Contains(got, "Checking event routing") {
		t.Fatalf("summary missing: %s", got)
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t", "status": "completed"}})
	if got := ansi.Strip(u.agents.agentState(*u.session.agent("/root/reviewer"))); got != "done" {
		t.Fatalf("finished state: %q", got)
	}
}

func TestNativeRosterRoleLegendAndCompactSpacing(t *testing.T) {
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Responding: true},
		{Name: "/root/very_long_finished_agent", Role: "worker", Final: true},
		{Name: "/root/other_finished_agent", Role: "worker", Final: true},
	}})
	now := time.Now()
	compact := v.nativeRoster(100, 4, now, false)
	if got := ansi.Strip(compact[1]); !strings.HasPrefix(got, " ◐ main  working") {
		t.Fatalf("folded names left padding in compact row: %q", got)
	}
	if got := ansi.Strip(strings.Join(compact, "\n")); strings.Contains(got, "Roles:") || !strings.Contains(got, "2 finished") {
		t.Fatalf("compact roster: %s", got)
	}
	for _, width := range []int{24, 60, 140} {
		for _, limit := range []int{2, 4, 8} {
			lines := v.nativeRoster(width, limit, now, true)
			if len(lines) > limit+1 {
				t.Fatalf("roster exceeded row budget: %d > %d", len(lines), limit+1)
			}
			legend := lines[len(lines)-1]
			if strings.Count(ansi.Strip(legend), "worker") != 1 || !strings.Contains(legend, v.roleColor("worker")+"●") {
				t.Fatalf("missing or duplicate role mapping: %q", legend)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("row overflow at width %d: %q", width, line)
				}
			}
			for _, hit := range v.hits {
				if hit.row >= len(lines) || strings.Contains(ansi.Strip(lines[hit.row-1]), "worker") {
					t.Fatalf("role text in agent row or legend hit: %+v", hit)
				}
			}
		}
	}
	for _, agent := range v.agents[1:] {
		if got := v.nativeGlyph(agent); got != v.roleColor("worker")+"✓"+liveActivityReset {
			t.Fatalf("status did not preserve role color and completion shape: %q", got)
		}
	}
}

func TestNativeRosterOverflowLegendKeepsSelectedRole(t *testing.T) {
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root"},
		{Name: "/root/a", Role: "review-correctness"},
		{Name: "/root/b", Role: "review-simplify"},
		{Name: "/root/c", Role: "explorer"},
		{Name: "/root/d", Role: "worker"},
	}})
	for _, agent := range v.agents[1:] {
		v.selected = agent.Name
		lines := v.nativeRoster(60, 8, time.Now(), true)
		legend := lines[len(lines)-1]
		if !strings.HasPrefix(ansi.Strip(legend), " Roles: ● "+agent.Role) || !strings.Contains(legend, v.roleColor(agent.Role)+"●") {
			t.Fatalf("selected role lost in overflowing legend: %q", legend)
		}
	}
}
