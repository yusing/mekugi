package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestNativeRosterShowsActivityDetails(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "reviewer", "agentRole": "review"}})
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "mcat " + thread + ".go"}})
	}
	got := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).NativeRoster(140, 6, time.Now(), true), "\n"))
	for _, want := range []string{"main.go", "child.go", "reviewer", "● review", "Read"} {
		if !strings.Contains(got, want) {
			t.Fatalf("roster missing %q: %s", want, got)
		}
	}
	appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "reasoning", "delta": "Checking event routing"})
	flushStreamOutput(&u.attached, u.shell, &u.Client, &u.streams, u.execTrack, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.ctx, u.retained, u.segments, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues)
	got = ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).NativeRoster(140, 6, time.Now(), true), "\n"))
	if !strings.Contains(got, "Checking event routing") {
		t.Fatalf("summary missing: %s", got)
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "t", "status": "completed"}})
	if got := ansi.Strip(u.shell.Agents.AgentState(u.testActivity(u.shell.Agents).testAgent(*u.attached.session.Agent("/root/reviewer")), 80, presentationTime(u.clock))); got != "done" {
		t.Fatalf("finished state: %q", got)
	}
}

func TestNativeRosterRoleLegendAndCompactSpacing(t *testing.T) {
	v := newActivityAdapter()
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Responding: true},
		{Name: "/root/very_long_finished_agent", Role: "worker", Final: true},
		{Name: "/root/other_finished_agent", Role: "worker", Final: true},
	}})
	now := time.Now()
	compact := v.NativeRoster(100, 4, now, false)
	if got := ansi.Strip(compact[1]); !strings.HasPrefix(got, " ◐ main  working") {
		t.Fatalf("agent names left padding in compact row: %q", got)
	}
	if got := ansi.Strip(strings.Join(compact, "\n")); strings.Contains(got, "Roles:") || !strings.Contains(ansi.Strip(compact[0]), "2 finished") || len(compact) != 4 || strings.Contains(got, ",") {
		t.Fatalf("compact roster: %s", got)
	}
	for _, width := range []int{24, 60, 140} {
		for _, limit := range []int{2, 4, 8} {
			lines := v.NativeRoster(width, limit, now, true)
			if len(lines) > limit+1 {
				t.Fatalf("roster exceeded row budget: %d > %d", len(lines), limit+1)
			}
			legend := lines[len(lines)-1]
			if strings.Count(ansi.Strip(legend), "worker") != 1 || !strings.Contains(legend, v.RoleColor("worker")+"●") {
				t.Fatalf("missing or duplicate role mapping: %q", legend)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("row overflow at width %d: %q", width, line)
				}
			}
			for _, hit := range v.Hits {
				if hit.Row >= len(lines) || strings.Contains(ansi.Strip(lines[hit.Row-1]), "worker") {
					t.Fatalf("role text in agent row or legend hit: %+v", hit)
				}
			}
		}
	}
	for _, agent := range v.Agents[1:] {
		if got := v.NativeGlyph(v.testAgent(agent)); got != v.RoleColor("worker")+"✓"+terminalui.Reset {
			t.Fatalf("status did not preserve role color and completion shape: %q", got)
		}
	}
}

func TestNativeRosterOverflowLegendKeepsSelectedRole(t *testing.T) {
	v := newActivityAdapter()
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root"},
		{Name: "/root/a", Role: "review-correctness"},
		{Name: "/root/b", Role: "review-simplify"},
		{Name: "/root/c", Role: "explorer"},
		{Name: "/root/d", Role: "worker"},
	}})
	for _, agent := range v.Agents[1:] {
		v.displayThread(agent.Name)
		lines := v.NativeRoster(60, 8, time.Now(), true)
		legend := lines[len(lines)-1]
		if !strings.HasPrefix(ansi.Strip(legend), " Roles: ● "+agent.Role) || !strings.Contains(legend, v.RoleColor(agent.Role)+"●") {
			t.Fatalf("selected role lost in overflowing legend: %q", legend)
		}
	}
}

func TestNativeRosterUnfocusedMetricsRightAligned(t *testing.T) {
	v := newActivityAdapter()
	now := time.Now()
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Responding: true, Started: now.Add(-13 * time.Second), WorkTimer: activitypresentation.WorkTimer{Known: true, Since: now.Add(-13 * time.Second)}, ContextKnown: true, ContextTokens: 47_600, ContextWindow: 200_000, InputTokens: 182_000, OutputTokens: 2_100, Turns: 1, Roundtrips: 1},
		{Name: "/root/tester", Responding: true, Started: now.Add(-10 * time.Second), WorkTimer: activitypresentation.WorkTimer{Known: true, Since: now.Add(-10 * time.Second)}},
		{Name: "/root/explorer_agent", Responding: true, Started: now.Add(-10 * time.Second), WorkTimer: activitypresentation.WorkTimer{Known: true, Since: now.Add(-10 * time.Second)}, ContextKnown: true, ContextTokens: 27_600, ContextWindow: 200_000},
	}})
	lines := v.NativeRoster(150, 8, now, false)
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
	v := newActivityAdapter()
	now := time.Now()
	agents := []sessionpresentation.ActivityAgent{
		{Name: "/root", Started: now.Add(-37 * time.Minute), WorkTimer: activitypresentation.WorkTimer{Known: true, ElapsedNS: int64(37 * time.Minute)}, ContextKnown: true, ContextTokens: 171_300, ContextWindow: 285_000, InputTokens: 7_500_000, OutputTokens: 30_000, Turns: 3, Roundtrips: 3},
		{Name: "/root/a", Started: now.Add(-94 * time.Second), WorkTimer: activitypresentation.WorkTimer{Known: true, ElapsedNS: int64(94 * time.Second)}, ContextKnown: true, ContextTokens: 24_400, ContextWindow: 285_000, InputTokens: 411_600, OutputTokens: 4_000, Turns: 1, Roundtrips: 1},
	}
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: agents})
	var parts [][activityui.MetricParts]string
	for _, agent := range agents {
		parts = append(parts, activityui.RosterMetricParts(v.View, v.testAgent(agent), now))
	}
	var rows []string
	for _, row := range activityui.RosterColumns(parts, 200) {
		rows = append(rows, ansi.Strip(row))
	}
	if ansi.StringWidth(rows[0]) != ansi.StringWidth(rows[1]) {
		t.Fatalf("rows differ in width:\n%s", strings.Join(rows, "\n"))
	}
	// Columns fit their widest values: "24.4K/285K • 9%", "1m34s · 1m34s ago",
	// "↑411.6K ↓30K" and "T+3", each after a two-column gap.
	if got := ansi.StringWidth(rows[0]); got != 4*2+14+17+13+3 {
		t.Fatalf("metric width = %d:\n%s", got, strings.Join(rows, "\n"))
	}
	for _, mark := range []string{"·", "↓", "T+"} {
		if !strings.Contains(rows[0], mark) || ansi.StringWidth(rows[0][:strings.Index(rows[0], mark)]) != ansi.StringWidth(rows[1][:strings.Index(rows[1], mark)]) {
			t.Fatalf("%q is not aligned:\n%s", mark, strings.Join(rows, "\n"))
		}
	}
}

func TestNativeRosterShowsEditedLines(t *testing.T) {
	v := newActivityAdapter()
	now := time.Now()
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Started: now.Add(-time.Minute), InputTokens: 2000, OutputTokens: 100},
		{Name: "/root/a", Started: now.Add(-time.Minute), InputTokens: 1000, OutputTokens: 100},
		{Name: "/root/b", Started: now.Add(-time.Minute)},
	}})
	chunk := func(caller, diff string, incomplete string) presentation.Chunk {
		return presentation.Chunk{Caller: caller, Review: diffmodel.ReviewFile{Diff: diff, Incomplete: incomplete}}
	}
	v.Model.LineCounts = presentation.CallerCounts([]presentation.File{
		{Chunks: []presentation.Chunk{chunk("/root", "@@ -1 +1,2 @@\n-a\n+b\n+c", ""), chunk("/root/a", "@@ -1 +1 @@\n-x\n+y", ""), chunk("/root/a", "", "tool-managed coverage")}},
		{Chunks: []presentation.Chunk{chunk("/root", "@@ -0,0 +1 @@\n+d", ""), chunk("/root/b", "", "truncated")}},
	})
	v.Model.NetCounts = new(diffmodel.Counts{Added: 2, Removed: 1})
	lines := v.NativeRoster(150, 8, now, true)
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
	delete(v.Model.LineCounts, "/root/b")
	if header := ansi.Strip(v.NativeRoster(150, 8, now, true)[0]); !strings.Contains(header, "+2 -1 · ↑3K") {
		t.Fatalf("session total missing: %q", header)
	}
}

// Rewritten scratch files outside the workspace are not project edits.
func TestLiveDiffCallerCountsSkipFilesOutsideWorkspace(t *testing.T) {
	chunk := func(caller, workspace, before, after string) presentation.Chunk {
		return presentation.Chunk{Workspace: workspace, Caller: caller,
			Review: diffmodel.ReviewFile{BeforePath: before, AfterPath: after, Diff: "@@ -1 +1 @@\n-x\n+y"}}
	}
	counts := presentation.CallerCounts([]presentation.File{{Chunks: []presentation.Chunk{
		chunk("/root", "/w", "/w/a.go", "/w/a.go"),
		chunk("/root", "/w", "/tmp/stats.txt", "/tmp/stats.txt"),
		chunk("/root", "/w", "/w-other/b.go", "/w-other/b.go"),
		chunk("/root", "/w", "/w/moved.go", "/tmp/moved.go"),
		chunk("/root", "", "/tmp/unknown.go", "/tmp/unknown.go"), // Unknown workspace still counts.
		chunk("/root/scratch", "/w", "", "/tmp/new.txt"),
	}}})
	if got, want := counts["/root"], (diffmodel.Counts{Added: 3, Removed: 3}); got != want {
		t.Fatalf("/root counts = %+v, want %+v", got, want)
	}
	if _, ok := counts["/root/scratch"]; ok {
		t.Fatalf("an agent with only scratch edits has counts: %+v", counts)
	}
}

func TestNativeRosterMetricsEaseToNewValues(t *testing.T) {
	v := newActivityAdapter()
	now := time.Now()
	agent := sessionpresentation.ActivityAgent{Name: "/root", Started: now.Add(-time.Minute), Turns: 1, CostKnown: true, Cost: 1, InputTokens: 100_000, OutputTokens: 1000}
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{agent}})
	v.Model.LineCounts = map[string]diffmodel.Counts{"/root": {Added: 10, Removed: 2}}
	row := func(at time.Time) string {
		t.Helper()
		return ansi.Strip(v.NativeRoster(150, 8, at, true)[1])
	}
	// Restored or first-seen values show at once.
	if got := row(now); !strings.Contains(got, "+10 -2") || !strings.Contains(got, "↑100K") || !strings.Contains(got, "$1.00") || v.RosterEasing {
		t.Fatalf("first frame eased: %q", got)
	}
	agent.InputTokens, agent.Cost = 200_000, 2
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{agent}})
	v.Model.LineCounts = map[string]diffmodel.Counts{"/root": {Added: 110, Removed: 2}}
	row(now)
	mid := row(now.Add(activityui.MetricEase / 4))
	if strings.Contains(mid, "↑100K") || strings.Contains(mid, "↑200K") || strings.Contains(mid, "+10 ") || strings.Contains(mid, "+110") || strings.Contains(mid, "$1.00") || strings.Contains(mid, "$2.00") || !v.RosterEasing {
		t.Fatalf("metrics jumped instead of easing: %q", mid)
	}
	if got := row(now.Add(activityui.MetricEase)); !strings.Contains(got, "+110 -2") || !strings.Contains(got, "↑200K") || !strings.Contains(got, "$2.00") || v.RosterEasing {
		t.Fatalf("metrics did not settle: %q", got)
	}
	// A change mid-ease continues from the value on screen.
	agent.InputTokens = 300_000
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{agent}})
	later := now.Add(activityui.MetricEase)
	row(later)
	agent.InputTokens = 400_000
	v.Coverage = ""
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{agent}})
	if got := row(later.Add(time.Millisecond)); !strings.Contains(got, "↑200K") && !strings.Contains(got, "↑200.") {
		t.Fatalf("retarget restarted from the new value: %q", got)
	}
}

func TestNativeRosterLineCountColorsAndZeros(t *testing.T) {
	v := newActivityAdapter()
	for _, tt := range []struct {
		count          diffmodel.Counts
		added, removed string
	}{
		{diffmodel.Counts{}, "", ""},
		{diffmodel.Counts{Added: -1, Removed: -1}, "", ""},
		{diffmodel.Counts{Added: 3}, terminalui.Green + "+3\x1b[39m", ""},
		{diffmodel.Counts{Removed: 2}, "", terminalui.Red + "-2\x1b[39m"},
		{diffmodel.Counts{Added: 3, Removed: 2}, terminalui.Green + "+3\x1b[39m", terminalui.Red + "-2\x1b[39m"},
	} {
		added, removed := v.LineCountParts(tt.count)
		if added != tt.added || removed != tt.removed {
			t.Fatalf("%+v: got %q %q", tt.count, added, removed)
		}
		view := newActivityAdapter()
		now := time.Now()
		view.Coverage = ""
		view.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root", Started: now, InputTokens: 100}}})
		view.Model.LineCounts = map[string]diffmodel.Counts{"/root": tt.count}
		rows := view.NativeRoster(150, 8, now, true)
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
