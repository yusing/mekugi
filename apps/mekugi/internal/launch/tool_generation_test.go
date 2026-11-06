package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func TestToolGenerationObservationRefreshesNativeRoster(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.controlObservations = new(sessioncontrol.Observations)
	u.attached.session.ToolInput = u.controlObservations.ToolInput
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	var generation sessioncontrol.Generation
	u.controlObservations.SetToolInput(&generation, "", "main", sessioncontrol.GenerationItem{ID: "item"}, true)
	u.controlObservations.IdentifyToolInput(&generation, sessioncontrol.GenerationItem{ID: "item"}, "exec", true, `await tools.mcp__threadcontrol__spawn_agent({message:"long`)
	refresh := func() string {
		applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
		publishActivity(&u.attached, u.shell)
		return ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 8, time.Now(), false), "\n"))
	}
	if rows := refresh(); !strings.Contains(rows, "Spawning subagent") {
		t.Fatalf("generation not delivered by normal observation refresh: %q", rows)
	}
	u.controlObservations.IdentifyToolInput(&generation, sessioncontrol.GenerationItem{ID: "item"}, "", false, `",task_name:"worker",more:`)
	if rows := refresh(); !strings.Contains(rows, "Spawning subagent worker") {
		t.Fatalf("name refinement not delivered: %q", rows)
	}
	u.controlObservations.EndGeneration(&generation)
	if rows := refresh(); strings.Contains(rows, "Spawning subagent") {
		t.Fatalf("generation survived transport completion: %q", rows)
	}
}
