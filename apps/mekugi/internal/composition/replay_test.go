package composition

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/conversation"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
)

func TestCompositionReplayRolloutAndOptionalTiming(t *testing.T) {
	for _, timing := range []bool{false, true} {
		t.Run(map[bool]string{false: "without timing", true: "with timing"}[timing], func(t *testing.T) {
			var c Composition
			history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
			replay := c.Add(sessionreplay.Module, func() sessionreplay.Deps { return sessionreplay.Deps{History: history.Value()} })
			// The contribution is collected even when its owner follows its host.
			if timing {
				c.Add(transportdiagnostics.Module, func() transportdiagnostics.Deps { return transportdiagnostics.Deps{} })
			}
			runtime, err := c.Activate(t.Context(), new(binding.Registry))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := runtime.Close(t.Context()); err != nil {
					t.Error(err)
				}
			})
			root := t.TempDir()
			path := filepath.Join(root, "rollout.jsonl")
			body := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"main\",\"cwd\":\"/recorded/workspace\"}}\n" +
				"{\"type\":\"event_msg\",\"timestamp\":\"2026-10-01T00:00:00Z\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"turn\"}}\n" +
				"{\"type\":\"event_msg\",\"timestamp\":\"2026-10-01T00:00:02Z\",\"payload\":{\"type\":\"task_complete\",\"turn_id\":\"turn\"}}\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if timing {
				data := `{"boundary":"provider","thread_id":"main","captured_at":"2026-10-01T00:00:01Z","duration_ms":500}`
				if err := os.WriteFile(filepath.Join(root, "capture.jsonl"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			source, err := replay.Value().Read(t.Context(), playback.ReadOptions{Path: path, DebugDirectory: root, Seed: 1})
			if err != nil {
				t.Fatal(err)
			}
			if source.Thread != "main" || source.Cwd != "/recorded/workspace" || source.End.Sub(source.Start) != 2*time.Second {
				t.Fatalf("decoded source=%+v", source)
			}
			if timing && source.Providers != 1 || !timing && source.Providers != 0 {
				t.Fatalf("provider intervals=%d", source.Providers)
			}
		})
	}
}

func TestCompositionConversationTranscriptWithoutActivity(t *testing.T) {
	for _, withActivity := range []bool{false, true} {
		var c Composition
		conversation := c.Add(conversation.Module, func() conversation.Deps { return conversation.Deps{} })
		if withActivity {
			c.Add(activity.Module, func() activity.Deps { return activity.Deps{} })
		}
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := runtime.Close(t.Context()); err != nil {
				t.Error(err)
			}
		})
		var store scope.Store
		if runtime.Observation == nil || runtime.Observation.NewTranscript(&store) != conversation.Value().Transcript(&store) {
			t.Fatalf("Activity=%v: transcript did not use composed Conversation", withActivity)
		}
	}
}
