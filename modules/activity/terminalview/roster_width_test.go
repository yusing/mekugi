package terminalview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

func TestRosterVisibleNamesDetermineWidth(t *testing.T) {
	now := time.Unix(100, 0)
	const width = 140
	for _, mode := range []string{"native", "rows", "cards", "unfocused"} {
		t.Run(mode, func(t *testing.T) {
			var rows []presentation.AgentRow
			for i, name := range []string{"a", "bb", "ccc", "dddd", "hidden"} {
				rows = append(rows, presentation.AgentRow{Agent: presentation.Agent{
					AgentFacts: presentation.AgentFacts{Name: "/root/" + name, Responding: true, Started: now,
						WorkKnown: true, WorkElapsed: 5 * time.Second, TokensKnown: true, InputTokens: 1234, OutputTokens: 345,
						CostKnown: true, Cost: 0.01, Turns: 1, Roundtrips: 1},
					Status: "responding", Summary: []presentation.Entry{{Facts: presentation.Facts{Seq: uint64(i + 1)},
						Values: []presentation.Value{{Content: feed.Content{Kind: "text", Body: "Checking visible work and retaining useful source details"}}}}},
				}})
			}
			if mode == "unfocused" {
				rows[4].Agent.Responding, rows[4].Agent.Final, rows[4].Agent.Status = false, true, "answered"
			}
			render := func(rows []presentation.AgentRow) []string {
				v := NewView()
				v.Update(presentation.Publication{Roster: rows}, Display{Caller: "/root"})
				var lines []string
				switch mode {
				case "native":
					lines = v.NativeRoster(width, 3, now, true)
				case "unfocused":
					lines = v.NativeRoster(width, 3, now, false)
				case "rows":
					lines = v.RenderRoster(v.Roster(), width, 3, now)
				case "cards":
					lines = v.RenderCards(v.Roster(), width, 3, now)
				}
				var visible []string
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("roster row exceeds pane width: %q", line)
					}
					plain := ansi.Strip(line)
					if strings.Contains(plain, "Checking visible work") {
						visible = append(visible, plain)
					}
				}
				if len(visible) < 2 {
					t.Fatalf("missing visible agents and their summaries: %q", lines)
				}
				return visible
			}
			want := render(rows)
			longHidden := slices.Clone(rows)
			longHidden[4].Agent.Name = "/root/" + strings.Repeat("offscreen-name", 8)
			if got := render(longHidden); !slices.Equal(got, want) {
				t.Fatalf("hidden agent name changed visible layout:\n got %q\nwant %q", got, want)
			}
			rows[0].Agent.Name = "/root/visible-long-reviewer"
			if got := strings.Join(render(rows), "\n"); !strings.Contains(got, "visible-long-reviewer") {
				t.Fatalf("visible long name lost its available width: %q", got)
			}
		})
	}
}
