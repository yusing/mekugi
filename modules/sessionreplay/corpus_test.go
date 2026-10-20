package sessionreplay_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestDescriptorCorruptHooksCountAgainstCorpus(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed JSON": "{",
		"wrong identity": `{"Version":1,"Workspace":"other","Thread":"other"}`,
		"null record":    "null",
		"invalid UTF-8":  "\xff",
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			workspace := "/archived/workspace"
			threads := []string{"main", "child-a", "child-b", "child-c"}
			sources := make(map[string]observation.Source)
			for i, thread := range threads {
				path := filepath.Join(directory, "rollout-fixture-"+thread+".jsonl")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				item := observation.Item{ID: thread, Type: "agentMessage"}
				if i+1 < len(threads) {
					item.AgentThreadID = threads[i+1]
				}
				at := time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC)
				sources[path] = observation.Source{Thread: thread, Cwd: workspace, Events: []observation.Event{{
					At: at, Started: at, Kind: observation.ItemCompleted, ThreadID: thread, Item: item,
				}}}
				data := raw + strings.Repeat(" ", 300-len(raw))
				if err := os.WriteFile(filepath.Join(directory, history.HookObservationsName(workspace, thread)), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			decodedBytes := 1024
			script := &moduletest.Scripted{ReplaySource: binding.RolloutSource{
				Replay: func(_ context.Context, path string, _ map[string]bool) (observation.Source, error) {
					source := sources[path]
					source.Bytes = decodedBytes
					return source, nil
				},
				Identity: func(path string) (string, error) { return sources[path].Thread, nil },
			}}
			retained := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
			service := moduletest.New(t, sessionreplay.Module, sessionreplay.Deps{History: retained}, module.Slots{}, script)
			options := playback.ReadOptions{Path: filepath.Join(directory, "rollout-fixture-main.jsonl"), HistoryDirectory: directory}
			source, err := service.Read(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if len(source.HookUnavailable) != len(threads) || len(source.Events) != len(threads) || len(source.Missing) != 0 {
				t.Fatalf("corrupt hook coverage: gaps=%v events=%d missing=%v", source.HookUnavailable, len(source.Events), source.Missing)
			}
			for i, thread := range threads {
				if !strings.HasPrefix(source.HookUnavailable[i], thread+": ") {
					t.Fatalf("hook gap lost thread identity: %q", source.HookUnavailable[i])
				}
			}
			// Supply decoded source sizes at the binding boundary rather than
			// allocate a 256 MiB fixture. The fourth rejected hook crosses the
			// corpus bound; each individual source remains below 64 MiB.
			decodedBytes = (64 << 20) - 256
			source, err = service.Read(t.Context(), options)
			if source != nil || err == nil || !strings.Contains(err.Error(), "replay corpus exceeds 256 MiB") {
				t.Fatalf("corrupt hook bytes escaped corpus accounting: source returned=%t error=%v", source != nil, err)
			}
		})
	}
}
