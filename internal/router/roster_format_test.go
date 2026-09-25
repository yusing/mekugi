package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestRosterSummaryFormatting(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := liveActivityPainter{theme: theme}
		for _, source := range []string{"Run `git status --short`", "Run\n```bash\ngit status --short\n```"} {
			summary := p.summary(parseLiveActivity(activityPaneEntry{Kind: "tool", Text: source}))
			command := strings.TrimPrefix(summary, liveActivitySummaryVerb("Run"))
			if ansi.Strip(summary) != "Run git status --short" || command == ansi.Strip(command) {
				t.Fatalf("command lost syntax highlighting: %q", summary)
			}
		}
		summary := p.summary(parseLiveActivity(activityPaneEntry{Kind: "final", Text: "- **Herdr launch ownership:** preserved."}))
		if summary != "Herdr launch ownership: preserved." {
			t.Fatalf("list summary = %q", summary)
		}
		for _, recipient := range []string{"/root", "/root/worker", "/root/parent/worker"} {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "reply", Text: "[`/root/replier` -> `" + recipient + "`] Message received:\nDone."})
			rows := strings.Join(p.block(blocks[0], 80), "\n")
			summary := p.summary(blocks)
			for _, text := range []string{ansi.Strip(rows), ansi.Strip(summary)} {
				if strings.Contains(text, "/root") || strings.Contains(text, "replier") || !strings.Contains(text, "to "+agentDisplayName(recipient)) {
					t.Fatalf("reply = %q", text)
				}
			}
		}
	}
}

func TestRosterMetricsInlineAndHitTargets(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b", "/root/c")
	v.agents[0].Role = "explorer"
	v.agents[0].InputTokens, v.agents[0].OutputTokens = 1000, 20
	v.agents[0].Turns, v.agents[0].CostKnown, v.agents[0].Cost = 1, true, .25
	lines := plainLines(v.renderRosterPane(120, 8, time.Now()))
	// Metrics share the agent's row, in columns aligned across rows.
	if !strings.Contains(lines[1], "Read a.go") || strings.Contains(lines[1], "explorer") || !strings.Contains(strings.Join(strings.Fields(lines[1]), " "), "↑ 1K ↓ 20 $0.25 T+1") ||
		!strings.Contains(lines[2], "Read b.go") || strings.Index(lines[1], "now") != strings.Index(lines[2], "now") {
		t.Fatalf("roster = %q", lines)
	}
	for row, want := range map[int]string{2: "/root/a", 3: "/root/b"} {
		v.pointAgent('h', row, 5)
		if hit := v.hovered; hit != want {
			t.Fatalf("row %d hit = %q, want %q", row, hit, want)
		}
	}
	v.selected = "/root/c"
	for _, height := range []int{2, 3, 4, 5, 8} {
		lines := v.renderRosterPane(35, height, time.Now())
		if len(lines) != height || !strings.Contains(strings.Join(plainLines(lines), "\n"), "c") {
			t.Fatalf("height %d: %q", height, lines)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > 34 {
				t.Fatalf("overflow: %q", line)
			}
		}
	}
}

func TestRosterSharesTokenReportCost(t *testing.T) {
	a := newSubagentActivity()
	a.observe("root", "", "/root", false)
	a.attachPane(newActivityPane(t.Context(), func() bool { return true }))
	a.pane.root = "root"
	usage := newThreadUsage()
	a.usage = usage
	for _, tier := range []string{"priority", "default"} {
		request, err := parseResponsesRequest([]byte(`{"model":"gpt-6-sol","reasoning":{"effort":"high"},"service_tier":"priority","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		observation := usage.observation("root", "root", request.model(), "priority")
		observation.reasoning = request.reasoningEffort()
		counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10, ServiceTier: tier}
		observation.observe(counts)
		report, _ := usage.snapshot("root")
		a.syncUsage("root")
		agent := a.paneAgentsLocked()[0]
		want := "gpt-6-sol high"
		if tier == "priority" {
			want += " [fast]"
		}
		if !strings.Contains(report.model, want) || agent.Cost != report.cost.cachedInput+report.cost.uncachedInput+report.cost.output {
			t.Fatalf("agent=%+v report=%+v", agent, report)
		}
	}
}

func TestRosterRoleUsesDurableSpawnEvidence(t *testing.T) {
	p := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, p, "root-session", "root", "", "/root", nil)
	if err := p.journals.bindSpawnRoles(t.Context(), p.replayStore, root.directory, "root", map[string]journalSpawnRole{"/root/worker": {Role: "explorer"}}); err != nil {
		t.Fatal(err)
	}
	child, _ := prepareActivityTest(t, p, "child-session", "child", "root", "/root/worker", nil)
	node := p.activity.threads["child"]
	if node.role != "explorer" {
		t.Fatalf("node = %+v", node)
	}
	child.Close()
	root.Close()
	if err := p.journals.bindSpawnRoles(t.Context(), p.replayStore, root.directory, "root", map[string]journalSpawnRole{"/root/worker": {Role: "worker"}}); err != nil {
		t.Fatal(err)
	}
	prepareActivityTest(t, p, "child-session-2", "child", "root", "/root/worker", nil)
	if node.role != "" {
		t.Fatalf("conflicted role = %q", node.role)
	}
}

func TestRosterStripPreservesSelectionStyling(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b")
	v.selected, v.hovered = "/root/a", "/root/b"
	line := v.renderStrip(v.roster(), 80)
	for _, name := range []string{"a", "b"} {
		if !strings.Contains(line, "\x1b[4m"+name+"\x1b[24m") || !strings.Contains(line, liveAgentColor("/root/"+name)) {
			t.Fatalf("strip lost selection or color: %q", line)
		}
	}
}
