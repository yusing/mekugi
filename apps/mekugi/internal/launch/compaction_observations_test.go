package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestCompactionMeasurementsRetainRestoreReplay(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	u := newAppServerSessionTestUI(t, "/workspace/replay")
	u.retainedSession = history.Session{Store: p.retained.Store, Thread: "main", Context: t.Context()}
	at := time.UnixMilli(replayTestEpoch)
	u.clock = func() time.Time { return at }
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	agent := u.attached.session.Agent("/root")
	agent.ContextKnown, agent.ContextTokens, agent.ContextWindow = true, 120000, 200000
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "compact", Type: "contextCompaction", Status: "inProgress"}})
	at = at.Add(12 * time.Second)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "compact", Type: "contextCompaction", Status: "completed"}})
	retained, _, err := p.retained.Store.ReadCompaction("main", "compact")
	if err != nil || retained == nil || retained.Context == nil || retained.Context.Tokens != 120000 || retained.ElapsedNS == nil || time.Duration(*retained.ElapsedNS) != 12*time.Second {
		t.Fatalf("missing completed boundary measurements: %+v, %v", retained, err)
	}
	if foreign, _, err := p.retained.Store.ReadCompaction("other", "compact"); err != nil || foreign != nil {
		t.Fatalf("foreign measurement: %+v, %v", foreign, err)
	}
	want := u.attached.source.Entries[0].Values[0].Label
	if retained.Method != observation.ContextReset || !strings.HasPrefix(want, "Context reset") || u.attached.source.Entries[0].Values[0].Disclosure() != nil {
		t.Fatal("live reset lost method or exposed a summary")
	}
	restored := newAppServerSessionTestUI(t, "/workspace/replay")
	restored.retainedSession = u.retainedSession
	restored.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{{ID: "compact", Type: "contextCompaction", Status: "completed"}}}})
	restoreCompactionObservations(&restored.attached, restored.retainedSession, &appServerActivityRestore{})
	if got := restored.attached.source.Entries[0].Values[0].Label; got != want {
		t.Fatalf("restore label %q, live %q", got, want)
	}
	if restored.attached.source.Entries[0].Values[0].Disclosure() != nil {
		t.Fatal("restored reset exposed a summary")
	}
	path := replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("main", "turn", replayTestEpoch+1, replayTestEpoch+12000, map[string]any{"id": "compact", "type": "ContextCompaction"}),
		replayTestRecord("event_msg", replayTestEpoch+12001, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: p.retained.Store.Directory(), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	replay := newTestUIReplayPlayback(u.activity, t.Context(), source, 1)
	defer replay.close()
	if err := replay.seek(replay.Until); err != nil {
		t.Fatal(err)
	}
	replay.publish()
	found := false
	for _, entry := range replay.ui.source.Entries {
		if entry.Source.Kind == "compaction" {
			found = true
			if got := sessionpresentation.ActivityContent(entry.Source).Values()[0].Label; got != want {
				t.Fatalf("replay label %q, live %q", got, want)
			}
		}
	}
	if !found {
		t.Fatal("replay omitted compaction")
	}
}

func TestChildWorkspaceCompactionReplay(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	u := newAppServerSessionTestUI(t, "/workspace/parent")
	u.retainedSession = history.Session{Store: p.retained.Store, Thread: "main", Context: t.Context()}
	at := time.UnixMilli(replayTestEpoch)
	u.clock = func() time.Time { return at }
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", Cwd: "/workspace/child", AgentNickname: "worker"})
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "turn"}})
	agent := u.attached.session.Agent(u.attached.session.KnownCaller("child"))
	agent.ContextKnown, agent.ContextTokens, agent.ContextWindow = true, 120000, 200000
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "turn", "item": binding.ThreadItem{ID: "compact", Type: "contextCompaction", Status: "inProgress"}})
	at = at.Add(12 * time.Second)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "turn", "item": binding.ThreadItem{ID: "compact", Type: "contextCompaction", Status: "completed"}})
	path := replayTestWrite(t, t.TempDir(), "rollout-child.jsonl", replayTestRecord("session_meta", replayTestEpoch, map[string]any{"id": "child", "cwd": "/workspace/child"}),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("child", "turn", replayTestEpoch+1, replayTestEpoch+12000, map[string]any{"id": "compact", "type": "ContextCompaction"}))
	source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: p.retained.Store.Directory(), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range source.Events {
		if e.Kind == "item/completed" && e.Item.ID == "compact" {
			if e.Item.CompactionContext == nil || e.Item.CompactionContext.Tokens != 120000 || e.Item.DurationMS == nil || *e.Item.DurationMS != 12000 {
				t.Fatalf("child replay lost live measurements: %+v", e.Item)
			}
			return
		}
	}
	t.Fatal("replay omitted completion")
}

func TestUnavailableCompactionMeasurementAllowsReplay(t *testing.T) {
	for _, data := range []string{"{", `{"Thread":"other","Item":"compact"}`} {
		t.Run(data, func(t *testing.T) {
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			name := filepath.Join(p.retained.Store.Directory(), history.CompactionName("main", "compact"))
			if err := os.WriteFile(name, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			path := replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"),
				replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
				replayTestItem("main", "turn", replayTestEpoch+1, replayTestEpoch+12000, map[string]any{"id": "compact", "type": "ContextCompaction"}))
			source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: p.retained.Store.Directory(), Seed: 1})
			if err != nil {
				t.Fatalf("optional measurements prevented valid playback: %v", err)
			}
			u := newAppServerSessionTestUI(t, "/workspace/replay")
			replay := newTestUIReplayPlayback(u.activity, t.Context(), source, 1)
			defer replay.close()
			if err := replay.seek(replay.Until); err != nil {
				t.Fatal(err)
			}
			for _, entry := range replay.ui.source.Entries {
				if entry.Source.Kind == "compaction" {
					if entry.Source.Native.CompactionContext != nil || !strings.Contains(entry.Source.Text, "Compaction measurements unavailable:") {
						t.Fatalf("unavailable evidence was lost: %+v", entry.Source)
					}
					return
				}
			}
			t.Fatal("playback omitted compaction")
		})
	}
}
