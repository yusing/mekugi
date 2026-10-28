package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestReplayRetainedNonAttachableRoster(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	retained, err := history.NewService(control.RecordKinds())
	check(err)
	store, err := retained.Open(t.Context(), t.TempDir())
	check(err)
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	check(err)
	t.Cleanup(session.Close)
	coordinator := control.New()
	check(coordinator.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "root-session", Task: "/root", Generation: 1, Status: control.Working, AudienceAdmitted: true}))
	caller := control.Identity{Thread: "root", Session: "root-session", Turn: "turn", Call: "spawn", Item: "exec"}
	check(coordinator.RequestStarted(t.Context(), caller, true))
	spawn, err := coordinator.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "worker", Message: "Work", Settings: control.Settings{Workspace: t.TempDir(), NonAttachable: true}})
	check(err)
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		effect, err := coordinator.ClaimEffect(t.Context(), "root", spawn.ID)
		check(err)
		if effect == nil || effect.Phase != phase {
			t.Fatalf("phase %s: %+v", phase, effect)
		}
		check(coordinator.EffectAcknowledged(t.Context(), "root", spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: "child-session", Turn: "child-turn"}))
	}
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
			"id": "spawn", "type": "mcpToolCall", "server": "threadcontrol", "tool": "spawn_agent", "status": "completed",
			"arguments": map[string]any{"task_name": "worker", "message": "Work"},
			"result":    map[string]any{"content": []map[string]string{{"type": "text", "text": `{"state":"confirmed","thread_id":"child","task_name":"/root/worker"}`}}},
		}),
		replayTestRecord("event_msg", replayTestEpoch+4000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: store.Directory(), Seed: 1, ThreadMetadata: replayThreadMetadata})
	check(err)
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	for range 2 {
		check(p.advance(0))
		if p.ui.session.Agent("/root/worker") != nil {
			t.Fatal("checkpoint membership appeared before the recorded spawn")
		}
		check(p.advance(2 * time.Second))
		painted := replayPlaybackTestPaint(t, p, 120, 28)
		if !strings.Contains(painted, "worker") {
			t.Fatalf("recorded nonattachable row missing from terminal frame:\n%s", painted)
		}
		agent := p.ui.session.Agent("/root/worker")
		if agent == nil || !agent.NonAttachable || agent.TokensKnown || agent.CostKnown || agent.ContextKnown {
			t.Fatalf("retained metadata or absent metrics lost: %+v", agent)
		}
		p.ui.agents.NativeRoster(120, 10, p.At, true)
		for _, hit := range p.ui.agents.Hits {
			if hit.Agent == "/root/worker" {
				p.ui.agents.PointAgent('\r', hit.Row, hit.First)
			}
		}
		p.ui.agents.Selected = "/root/worker"
		p.ui.agents.HandleRosterKey("a")
		if p.ui.agents.DisplayRequest != nil {
			t.Fatal("rendered nonattachable row emitted a navigation intent")
		}
		version := p.ui.presentation.View().Version
		check(p.applyInput(func(yield func(terminalsession.InputEffect) bool) {
			yield(terminalsession.InputEffect{Roster: &activityui.DisplayIntent{Caller: "/root/worker", Attachment: version.Attachment, Revision: version.Revision}})
		}))
		if p.ui.presentation.View().Displayed.ID != "root" {
			t.Fatal("replay admitted a nonattachable roster intent")
		}
	}
}
