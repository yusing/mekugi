package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeRosterClickSurvivesActivityPaint(t *testing.T) {
	for _, width := range []int{80, 140} {
		for _, focus := range []int{2, 3} {
			t.Run(fmt.Sprintf("width%d/focus%d", width, focus), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker", "agentRole": "worker"}})
				u.shell.focus = focus
				for _, wantOnly := range []bool{true, false} {
					var frame bytes.Buffer
					u.shell.paintedRows = nil
					if err := u.paint(&frame, width, 30); err != nil {
						t.Fatal(err)
					}
					if u.shell.layout.agents.w == 0 {
						t.Fatal("test did not paint Activity")
					}
					screen := vt.NewEmulator(width, 30)
					_, err := screen.Write(frame.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					rows := strings.Split(screen.String(), "\n")
					screen.Close()
					roster := u.shell.layout.roster
					y := -1
					for row := roster.y + 1; row < roster.y+roster.h; row++ {
						if strings.Contains(rows[row], "worker") && !strings.Contains(rows[row], "Roles:") {
							y = row
							break
						}
					}
					if y < 0 {
						t.Fatalf("worker row not painted: %s", strings.Join(rows, "\n"))
					}
					for _, suffix := range []string{"M", "m"} {
						if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", roster.x+5, y+1, suffix)); err != nil {
							t.Fatal(err)
						}
					}
					if u.agents.selected != "/root/worker" || u.agents.only != wantOnly {
						t.Fatalf("painted roster click lost: selected=%q only=%v, want only=%v", u.agents.selected, u.agents.only, wantOnly)
					}
				}
			})
		}
	}
}

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
		if got := v.nativeGlyph(agent); got != v.roleColor("worker")+"✓"+activityui.Reset {
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

func TestNativeRosterUnfocusedMetricsRightAligned(t *testing.T) {
	v := newLiveActivityView()
	now := time.Now()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Responding: true, Started: now.Add(-13 * time.Second), ContextKnown: true, ContextTokens: 47_600, ContextWindow: 200_000, InputTokens: 182_000, OutputTokens: 2_100, Turns: 1, Roundtrips: 1},
		{Name: "/root/tester", Responding: true, Started: now.Add(-10 * time.Second)},
		{Name: "/root/explorer_agent", Responding: true, Started: now.Add(-10 * time.Second), ContextKnown: true, ContextTokens: 27_600, ContextWindow: 200_000},
	}})
	lines := v.nativeRoster(150, 8, now, false)
	var rows []string
	for _, line := range lines[1:] {
		rows = append(rows, ansi.Strip(line))
	}
	// Compact columns stay flush with the right edge, and each separator
	// lines up across rows.
	for _, row := range rows {
		if got := ansi.StringWidth(row); got != 149 {
			t.Fatalf("row width = %d, want metrics ending at the edge:\n%s", got, strings.Join(rows, "\n"))
		}
	}
	if !strings.HasSuffix(rows[0], "  13s · 13s ago  ↑182K ↓2.1K  T+1") {
		t.Fatalf("metric columns pad beyond content: %q", rows[0])
	}
	want := ansi.StringWidth(rows[0][:strings.Index(rows[0], "·")])
	for _, row := range rows[1:] {
		if got := ansi.StringWidth(row[:strings.Index(row, "·")]); got != want {
			t.Fatalf("timer is not aligned:\n%s", strings.Join(rows, "\n"))
		}
	}
}

func TestNativeRosterMetricsAlignParts(t *testing.T) {
	v := newLiveActivityView()
	now := time.Now()
	agents := []activityPaneAgent{
		{Name: "/root", Started: now.Add(-37 * time.Minute), ContextKnown: true, ContextTokens: 171_300, ContextWindow: 285_000, InputTokens: 7_500_000, OutputTokens: 30_000, Turns: 3, Roundtrips: 3},
		{Name: "/root/a", Started: now.Add(-94 * time.Second), ContextKnown: true, ContextTokens: 24_400, ContextWindow: 285_000, InputTokens: 411_600, OutputTokens: 4_000, Turns: 1, Roundtrips: 1},
	}
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: agents})
	var parts [][nativeMetricParts]string
	for _, agent := range agents {
		parts = append(parts, nativeRosterMetricParts(v, agent, now))
	}
	var rows []string
	for _, row := range nativeRosterColumns(parts, 200) {
		rows = append(rows, ansi.Strip(row))
	}
	if ansi.StringWidth(rows[0]) != ansi.StringWidth(rows[1]) {
		t.Fatalf("rows differ in width:\n%s", strings.Join(rows, "\n"))
	}
	// Columns fit their widest values: "171.3K/285K • 60%", "1m34s · 1m34s ago",
	// "↑411.6K ↓30K" and "T+3", each after a two-column gap.
	if got := ansi.StringWidth(rows[0]); got != 4*2+17+17+12+3 {
		t.Fatalf("metric width = %d:\n%s", got, strings.Join(rows, "\n"))
	}
	for _, mark := range []string{"/", "•", "·", "↓", "T+"} {
		if !strings.Contains(rows[0], mark) || ansi.StringWidth(rows[0][:strings.Index(rows[0], mark)]) != ansi.StringWidth(rows[1][:strings.Index(rows[1], mark)]) {
			t.Fatalf("%q is not aligned:\n%s", mark, strings.Join(rows, "\n"))
		}
	}
}

func TestNativeRosterShowsEditedLines(t *testing.T) {
	v := newLiveActivityView()
	now := time.Now()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Started: now.Add(-time.Minute), InputTokens: 2000, OutputTokens: 100},
		{Name: "/root/a", Started: now.Add(-time.Minute), InputTokens: 1000, OutputTokens: 100},
		{Name: "/root/b", Started: now.Add(-time.Minute)},
	}})
	chunk := func(caller, diff string, incomplete string) livediff.Chunk {
		return livediff.Chunk{Origin: livediff.Origin{Caller: caller}, Review: mekugi.ReviewFile{Diff: diff, Incomplete: incomplete}}
	}
	v.lineCounts = liveDiffCallerCounts([]livediff.File{
		{Chunks: []livediff.Chunk{chunk("/root", "@@ -1 +1,2 @@\n-a\n+b\n+c", ""), chunk("/root/a", "@@ -1 +1 @@\n-x\n+y", ""), chunk("/root/a", "", "tool-managed coverage")}},
		{Chunks: []livediff.Chunk{chunk("/root", "@@ -0,0 +1 @@\n+d", ""), chunk("/root/b", "", "truncated")}},
	})
	v.netCounts = new(livediff.Counts{Added: 2, Removed: 1})
	lines := v.nativeRoster(150, 8, now, true)
	var rows []string
	for _, line := range lines {
		rows = append(rows, ansi.Strip(line))
	}
	if strings.Contains(rows[0], "?") || !strings.Contains(rows[0], "+2 -1") || strings.Contains(rows[0], "+4 -2") {
		t.Fatalf("session outcome was replaced by cumulative activity: %q", rows[0])
	}
	for i, want := range []string{"+3 -1  ↑2K", "+1 -1  ↑1K"} {
		if !strings.Contains(rows[i+1], want) {
			t.Fatalf("row %d lacks %q:\n%s", i+1, want, strings.Join(rows, "\n"))
		}
	}
	if strings.Contains(rows[3], "?") {
		t.Fatalf("unknown-only agent has a placeholder: %q", rows[3])
	}
	// The added and removed counts share a separator column across rows.
	if at := strings.Index(rows[1], " -1"); ansi.StringWidth(rows[1][:at]) != ansi.StringWidth(rows[2][:strings.Index(rows[2], " -1")]) {
		t.Fatalf("line counts are not aligned:\n%s", strings.Join(rows, "\n"))
	}
	delete(v.lineCounts, "/root/b")
	if header := ansi.Strip(v.nativeRoster(150, 8, now, true)[0]); !strings.Contains(header, "+2 -1 · ↑3K") {
		t.Fatalf("session total missing: %q", header)
	}
}

// Rewritten scratch files outside the workspace are not project edits.
func TestLiveDiffCallerCountsSkipFilesOutsideWorkspace(t *testing.T) {
	chunk := func(caller, workspace, before, after string) livediff.Chunk {
		return livediff.Chunk{Workspace: workspace, Origin: livediff.Origin{Caller: caller},
			Review: mekugi.ReviewFile{BeforePath: before, AfterPath: after, Diff: "@@ -1 +1 @@\n-x\n+y"}}
	}
	counts := liveDiffCallerCounts([]livediff.File{{Chunks: []livediff.Chunk{
		chunk("/root", "/w", "/w/a.go", "/w/a.go"),
		chunk("/root", "/w", "/tmp/stats.txt", "/tmp/stats.txt"),
		chunk("/root", "/w", "/w-other/b.go", "/w-other/b.go"),
		chunk("/root", "/w", "/w/moved.go", "/tmp/moved.go"),
		chunk("/root", "", "/tmp/unknown.go", "/tmp/unknown.go"), // Unknown workspace still counts.
		chunk("/root/scratch", "/w", "", "/tmp/new.txt"),
	}}})
	if got, want := counts["/root"], (livediff.Counts{Added: 3, Removed: 3}); got != want {
		t.Fatalf("/root counts = %+v, want %+v", got, want)
	}
	if _, ok := counts["/root/scratch"]; ok {
		t.Fatalf("an agent with only scratch edits has counts: %+v", counts)
	}
}

func TestNativeRosterMetricsEaseToNewValues(t *testing.T) {
	v := newLiveActivityView()
	now := time.Now()
	agent := activityPaneAgent{Name: "/root", Started: now.Add(-time.Minute), Turns: 1, CostKnown: true, Cost: 1, InputTokens: 100_000, OutputTokens: 1000}
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{agent}})
	v.lineCounts = map[string]livediff.Counts{"/root": {Added: 10, Removed: 2}}
	row := func(at time.Time) string {
		t.Helper()
		return ansi.Strip(v.nativeRoster(150, 8, at, true)[1])
	}
	// Restored or first-seen values show at once.
	if got := row(now); !strings.Contains(got, "+10 -2") || !strings.Contains(got, "↑100K") || !strings.Contains(got, "$1.00") || v.rosterEasing {
		t.Fatalf("first frame eased: %q", got)
	}
	agent.InputTokens, agent.Cost = 200_000, 2
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{agent}})
	v.lineCounts = map[string]livediff.Counts{"/root": {Added: 110, Removed: 2}}
	row(now)
	mid := row(now.Add(rosterMetricEase / 4))
	if strings.Contains(mid, "↑100K") || strings.Contains(mid, "↑200K") || strings.Contains(mid, "+10 ") || strings.Contains(mid, "+110") || strings.Contains(mid, "$1.00") || strings.Contains(mid, "$2.00") || !v.rosterEasing {
		t.Fatalf("metrics jumped instead of easing: %q", mid)
	}
	if got := row(now.Add(rosterMetricEase)); !strings.Contains(got, "+110 -2") || !strings.Contains(got, "↑200K") || !strings.Contains(got, "$2.00") || v.rosterEasing {
		t.Fatalf("metrics did not settle: %q", got)
	}
	// A change mid-ease continues from the value on screen.
	agent.InputTokens = 300_000
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{agent}})
	later := now.Add(rosterMetricEase)
	row(later)
	agent.InputTokens = 400_000
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{agent}})
	if got := row(later.Add(time.Millisecond)); !strings.Contains(got, "↑200K") && !strings.Contains(got, "↑200.") {
		t.Fatalf("retarget restarted from the new value: %q", got)
	}
}

func TestNativeRosterLineCountColorsAndZeros(t *testing.T) {
	v := newLiveActivityView()
	for _, tt := range []struct {
		count          livediff.Counts
		added, removed string
	}{
		{livediff.Counts{}, "", ""},
		{livediff.Counts{Added: -1, Removed: -1}, "", ""},
		{livediff.Counts{Added: 3}, activityui.Green + "+3\x1b[39m", ""},
		{livediff.Counts{Removed: 2}, "", activityui.Red + "-2\x1b[39m"},
		{livediff.Counts{Added: 3, Removed: 2}, activityui.Green + "+3\x1b[39m", activityui.Red + "-2\x1b[39m"},
	} {
		added, removed := v.lineCountParts(tt.count)
		if added != tt.added || removed != tt.removed {
			t.Fatalf("%+v: got %q %q", tt.count, added, removed)
		}
		view := newLiveActivityView()
		now := time.Now()
		view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root", Started: now, InputTokens: 100}}})
		view.lineCounts = map[string]livediff.Counts{"/root": tt.count}
		rows := view.nativeRoster(150, 8, now, true)
		for _, row := range rows[1:2] {
			if strings.Contains(row, "+0") || strings.Contains(row, "-0") {
				t.Fatalf("zero count rendered: %q", row)
			}
			for _, colored := range []string{tt.added, tt.removed} {
				if colored != "" && !strings.Contains(row, colored) {
					t.Fatalf("missing colored count %q in %q", colored, row)
				}
			}
		}
	}
}
