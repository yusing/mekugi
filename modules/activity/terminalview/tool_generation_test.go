package terminalview

import (
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotToolGenerationRoster(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			view := NewView()
			view.Clock = func() time.Time { return time.Unix(100, 0) }
			view.Painter.Theme = theme
			var rows []string
			for _, label := range []string{"Asking user question", "Spawning subagent", "Spawning subagent `worker`", "Emitting a patch", "Executing a command", "Calling `mcp__other__lookup`"} {
				agent := presentation.Agent{AgentFacts: presentation.AgentFacts{Generating: label, Responding: true}, Status: "responding"}
				rows = append(rows, view.AgentState(agent, 80, time.Unix(100, 0)))
			}
			rows = append(rows, "plain after generation")
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/tool_generation_%d.txt", theme), rows, 80)
		})
	}
}
