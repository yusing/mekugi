package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestRosterSummaryFormatting(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := activityui.Painter{Theme: theme}
		for _, source := range []string{"Run `git status --short`", "Run\n```bash\ngit status --short\n```"} {
			summary := p.Summary(parseLiveActivity(activityPaneEntry{Kind: "tool", Text: source}), 80)
			command := strings.TrimPrefix(summary, activityui.SummaryVerb("Run"))
			if ansi.Strip(summary) != "Run git status --short" || command == ansi.Strip(command) {
				t.Fatalf("command lost syntax highlighting: %q", summary)
			}
		}
		summary := p.Summary(parseLiveActivity(activityPaneEntry{Kind: "final", Text: "- **Herdr launch ownership:** preserved."}), 80)
		if summary != "Herdr launch ownership: preserved." {
			t.Fatalf("list summary = %q", summary)
		}
		for _, recipient := range []string{"/root", "/root/worker", "/root/parent/worker"} {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "reply", message: &activityMessage{from: "/root/replier", to: recipient, text: "Done."}})
			rows := strings.Join(p.Block(blocks[0], 80), "\n")
			summary := p.Summary(blocks, 80)
			for _, text := range []string{ansi.Strip(rows), ansi.Strip(summary)} {
				if strings.Contains(text, "/root") || strings.Contains(text, "replier") || !strings.Contains(text, "→ "+activityui.AgentDisplayName(recipient)) {
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
	v.agents[0].Turns, v.agents[0].Roundtrips, v.agents[0].CostKnown, v.agents[0].Cost = 1, 1, true, .25
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

func TestUISnapshotRosterStripSelectionStyling(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b")
	v.selected, v.hovered = "/root/a", "/root/b"
	uisnapshot.AssertTerminal(t, "testdata/snapshots/roster-strip-selection.txt", []string{v.renderStrip(v.roster(), 80), "plain after roster"}, 80)
}
