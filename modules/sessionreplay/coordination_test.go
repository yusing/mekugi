package sessionreplay_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestReplayDiscoversIndependentChildFromConfirmedMCPSpawn(t *testing.T) {
	for _, state := range []string{"confirmed", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			marker := filepath.Join(directory, "command-ran")
			rootPath := filepath.Join(directory, "rollout-retained-root-thread.jsonl")
			childPath := filepath.Join(directory, "rollout-retained-child-thread-17.jsonl")
			// Neither independent rollout carries native graph or source metadata.
			// Only the confirmed MCP result supplies the literal child/task mapping.
			result := fmt.Sprintf(`{"state":%q,"thread_id":"child-thread-17","task_name":"/root/check[exact]"}`, state)
			root := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root-thread","cwd":%q}}
{"timestamp":"2026-10-01T00:00:01Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"root-thread","turn_id":"root-turn","started_at_ms":1790812800000,"completed_at_ms":1790812801000,"item":{"id":"spawn-item","type":"mcpToolCall","server":"threadcontrol","tool":"spawn_agent","status":"completed","arguments":{"task_name":"requested-alias","message":"Inspect retained evidence."},"result":{"content":[{"type":"text","text":%q}]}}}}
`, directory, result)
			child := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"child-thread-17","cwd":%q}}
{"timestamp":"2026-10-01T00:00:02Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"child-thread-17","turn_id":"child-turn-original","started_at_ms":1790812801000,"completed_at_ms":1790812802000,"item":{"id":"child-command-original","type":"commandExecution","status":"completed","command":["touch",%q],"exit_code":0}}}
{"timestamp":"2026-10-01T00:00:03Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"child-thread-17","turn_id":"child-turn-original","started_at_ms":1790812802000,"completed_at_ms":1790812803000,"item":{"id":"child-answer-original","type":"agentMessage","text":"Independent retained answer."}}}
`, directory, marker)
			for path, content := range map[string]string{rootPath: root, childPath: child} {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise the composed Rollout binding with the real backend decoder,
			// without constructing any native runtime or execution owner.
			script := &moduletest.Scripted{ReplaySource: binding.RolloutSource{Replay: rollout.Replay, Identity: rollout.HeaderIdentity}}
			history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
			activityService := moduletest.New(t, activity.Module, activity.Deps{}, module.Slots{}, script)
			service := moduletest.New(t, sessionreplay.Module, sessionreplay.Deps{History: history, Activity: activityService}, module.Slots{}, script)
			source, err := service.Read(t.Context(), playback.ReadOptions{Path: rootPath, HistoryDirectory: filepath.Join(directory, "absent-history"), Seed: 17})
			if err != nil {
				t.Fatal(err)
			}
			wantThreads := map[string]string{"root-thread": "/root"}
			wantItems := 1
			if state == "confirmed" {
				wantThreads["child-thread-17"] = "/root/check[exact]"
				wantItems = 3
			}
			if !reflect.DeepEqual(source.Threads, wantThreads) || len(source.Missing) != 0 || source.Items != wantItems {
				t.Fatalf("threads=%v missing=%v items=%d; want threads=%v items=%d", source.Threads, source.Missing, source.Items, wantThreads, wantItems)
			}
			foundAnswer := false
			for _, event := range source.Events {
				if event.Kind != observation.ItemCompleted || event.ItemID != "child-answer-original" {
					continue
				}
				foundAnswer = true
				if event.ThreadID != "child-thread-17" || event.TurnID != "child-turn-original" || event.Item.ID != "child-answer-original" || event.Item.Text != "Independent retained answer." {
					t.Fatalf("child answer lost retained identity or content: %+v", event)
				}
			}
			if foundAnswer != (state == "confirmed") {
				t.Fatalf("child answer admitted=%v for %s spawn", foundAnswer, state)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replay executed retained command: marker stat=%v", err)
			}
		})
	}
}
