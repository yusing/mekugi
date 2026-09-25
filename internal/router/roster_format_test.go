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
				if strings.Contains(text, "/root") || strings.Contains(text, "replier") || !strings.Contains(text, "→ "+liveActivityDisplayName(recipient)) {
					t.Fatalf("reply = %q", text)
				}
			}
		}
	}
}

func TestRosterMetricsSecondLineAndHitTargets(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b", "/root/c")
	v.agents[0].Configuration = "gpt-6-sol high [fast]"
	v.agents[0].Role = "explorer"
	v.agents[0].InputTokens, v.agents[0].OutputTokens = 1000, 20
	v.agents[0].Turns, v.agents[0].CostKnown, v.agents[0].Cost = 1, true, .25
	lines := plainLines(v.renderRosterPane(120, 8, time.Now()))
	if strings.Contains(lines[1], "gpt-") || !strings.Contains(lines[1], "Read a.go") || !strings.Contains(lines[2], "gpt-6-sol high [fast] · explorer") || !strings.Contains(lines[2], "↑ 1K ↓ 20 · $0.2500 · 1 turns") {
		t.Fatalf("roster = %q", lines)
	}
	for _, row := range []int{2, 3} {
		v.pointAgent('h', row, 5)
		if hit := v.hovered; hit != "/root/a" {
			t.Fatalf("row %d hit = %q", row, hit)
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

func TestRosterSharesTokenReportMetadataAndCost(t *testing.T) {
	a := newSubagentActivity()
	a.observe("root", "", "/root", false)
	a.attachPane(newActivityPane(t.Context(), func() bool { return true }))
	a.pane.root = "root"
	usage := newThreadUsage()
	for _, tier := range []string{"priority", "default"} {
		request, err := parseResponsesRequest([]byte(`{"model":"gpt-6-sol","reasoning":{"effort":"high"},"service_tier":"priority","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		a.syncPaneConfiguration("root", &request)
		if got := a.paneAgentsLocked()[0].Configuration; got != "gpt-6-sol high [fast]" {
			t.Fatal(got)
		}
		observation := usage.observation("root", "root", request.model(), "priority")
		observation.reasoning = request.reasoningEffort()
		counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10, ServiceTier: tier}
		observation.observe(counts)
		report, ok := usage.snapshot("root")
		a.syncUsage("root", counts, report, ok, tokenCost{})
		agent := a.paneAgentsLocked()[0]
		want := "gpt-6-sol high"
		if tier == "priority" {
			want += " [fast]"
		}
		if agent.Configuration != want || !strings.Contains(report.model, want) || agent.Cost != report.cost.cachedInput+report.cost.uncachedInput+report.cost.output || agent.Role != "main" {
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
	if node.role != "explorer" || node.configuration != "gpt-test" {
		t.Fatalf("node = %+v", node)
	}
	child.Close()
	root.Close()
	if err := p.journals.bindSpawnRoles(t.Context(), p.replayStore, root.directory, "root", map[string]journalSpawnRole{"/root/worker": {Role: "worker"}}); err != nil {
		t.Fatal(err)
	}
	prepareActivityTest(t, p, "child-session-2", "child", "root", "/root/worker", nil)
	if node.role != "n/a" {
		t.Fatalf("conflicted role = %q", node.role)
	}
}

func TestRosterStripPreservesSelectionStyling(t *testing.T) {
 v := liveActivityTestView("/root/a", "/root/b")
 v.selected, v.hovered = "/root/a", "/root/b"
 line := v.renderStrip(v.roster(), 80)
 for _, name := range []string{"a", "b"} {
  if !strings.Contains(line, "\x1b[4m"+name+"\x1b[24m") || !strings.Contains(line,liveAgentColor("/root/"+name)) { t.Fatalf("strip lost selection or color: %q",line) }
 }
}
