package sessionreplay_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func replayService(t *testing.T, decoded observation.Source, slots module.Slots) *playback.Service {
	t.Helper()
	script := &moduletest.Scripted{ReplaySource: binding.RolloutSource{
		Replay: func(ctx context.Context, _ string, _ map[string]bool) (observation.Source, error) {
			return decoded, ctx.Err()
		},
		Identity: func(string) (string, error) { return "", errors.New("unexpected sibling read") },
	}}
	history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
	service := moduletest.New(t, sessionreplay.Module, sessionreplay.Deps{History: history}, slots, script)
	if script.RolloutRegistrations != 1 {
		t.Fatalf("rollout registrations=%d", script.RolloutRegistrations)
	}
	return service
}

func TestDescriptorDecodedSourceStreamingAndOptionalTiming(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Second)
	text := strings.Repeat("recorded 世界 ", 30)
	decoded := observation.Source{Thread: "main", Cwd: "/archived/workspace", Events: []observation.Event{
		{At: start, Kind: observation.TurnStarted, ThreadID: "main", TurnID: "turn", TurnStatus: "inProgress"},
		{At: end, Started: start, Kind: observation.ItemCompleted, ThreadID: "main", TurnID: "turn", ItemID: "answer", Item: observation.Item{ID: "answer", Type: "agentMessage", Text: text}},
	}}
	options := playback.ReadOptions{Path: "scripted-rollout", HistoryDirectory: filepath.Join(t.TempDir(), "missing-history"), DebugDirectory: "not-opened-without-contributor", Seed: 17}
	service := replayService(t, decoded, module.Slots{})
	source, err := service.Read(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(options.HistoryDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline read created storage: %v", err)
	}
	if source.Providers != 0 || source.Items != 1 || source.Thread != "main" {
		t.Fatalf("source=%+v", source)
	}
	var streamed strings.Builder
	for _, event := range source.Events {
		switch event.Kind {
		case observation.ItemStarted:
			if event.Item.Text != "" || event.Item.DurationMS != nil {
				t.Fatal("item start exposed completed content")
			}
		case observation.AgentDelta:
			if !event.At.After(start) || !event.At.Before(end) || !utf8.ValidString(event.Delta) {
				t.Fatalf("invalid synthetic delta: %+v", event)
			}
			streamed.WriteString(event.Delta)
		}
	}
	if streamed.String() != text {
		t.Fatal("synthetic text differs from retained text")
	}
	same, err := service.Read(t.Context(), options)
	if err != nil || !reflect.DeepEqual(source.Events, same.Events) {
		t.Fatalf("seeded replay differs: %v", err)
	}
	called := false
	service = replayService(t, decoded, module.Slots{ReplayTiming: []replaytiming.Reader{func(_ context.Context, path string, threads map[string]bool, observe func(replaytiming.Interval) error) error {
		called = true
		if path != filepath.Join(options.DebugDirectory, "capture.jsonl") || !threads["main"] {
			t.Fatal("timing received wrong source identity")
		}
		return observe(replaytiming.Interval{Thread: "main", Start: start.Add(-time.Second), End: start})
	}}})
	source, err = service.Read(t.Context(), options)
	if err != nil || !called || source.Providers != 1 || !source.Start.Equal(start.Add(-time.Second)) {
		t.Fatalf("optional timing missing: %v", err)
	}
}

func TestPlaybackSemanticIntentsResetAndRecordedClock(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	source := &playback.Source{Start: start, End: start.Add(30 * time.Second)}
	for _, second := range []int{0, 5, 15, 25} {
		source.Events = append(source.Events, playback.Event{At: start.Add(time.Duration(second) * time.Second), ThreadID: "main", ItemID: time.Duration(second).String()})
	}
	player := playback.NewPlayer(source, 1)
	if err := player.Bounds(2*time.Second, 28*time.Second); err != nil {
		t.Fatal(err)
	}
	var shown []string
	resets := 0
	reset := func() { resets++; shown = nil }
	apply := func(event playback.Event) error {
		if !player.At.Equal(event.At) {
			t.Fatal("application observed wall time instead of recorded time")
		}
		shown = append(shown, event.ItemID)
		return nil
	}
	step := func(elapsed time.Duration, intent playback.Intent) {
		t.Helper()
		if _, err := player.Step(t.Context(), elapsed, intent, reset, apply); err != nil {
			t.Fatal(err)
		}
	}
	step(3*time.Second, playback.Faster)
	if player.Position != 3*time.Second || player.Speed != 2 {
		t.Fatal("speed change charged elapsed time at new speed")
	}
	step(time.Second, playback.TogglePause)
	step(time.Hour, playback.Tick)
	if player.Position != 5*time.Second {
		t.Fatal("pause advanced the clock")
	}
	step(0, playback.SeekForward)
	if player.Position != 15*time.Second || len(shown) != 3 {
		t.Fatal("forward seek lost intervening observations")
	}
	step(0, playback.SeekBackward)
	if player.Position != 5*time.Second || resets != 1 || len(shown) != 2 {
		t.Fatal("backward seek retained future presentation")
	}
	step(0, playback.Restart)
	if player.Position != player.From || player.Paused || resets != 2 || len(shown) != 1 {
		t.Fatal("restart did not reconstruct from the interval start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := player.Advance(ctx, player.Until, reset, apply); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestDescriptorRejectsMissingAndConflictingBindings(t *testing.T) {
	script := new(moduletest.Scripted)
	history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
	if _, err := sessionreplay.Module.New(sessionreplay.Deps{}, module.Slots{}); err == nil {
		t.Fatal("missing history accepted")
	}
	read := func(context.Context, string, map[string]bool, func(replaytiming.Interval) error) error { return nil }
	if _, err := sessionreplay.Module.New(sessionreplay.Deps{History: history}, module.Slots{ReplayTiming: []replaytiming.Reader{read, read}}); err == nil {
		t.Fatal("ambiguous timing accepted")
	}
	service, err := sessionreplay.Module.New(sessionreplay.Deps{History: history}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(t.Context(), playback.ReadOptions{}); err == nil {
		t.Fatal("unbound source accepted")
	}
	if err := sessionreplay.Module.Bindings.Register(script, service, nil); err == nil {
		t.Fatal("incomplete scripted binding accepted")
	}
}

func TestDescriptorReadCancellationAtSourceBoundaries(t *testing.T) {
	for _, boundary := range []string{"decoded rollout", "optional timing"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			script := &moduletest.Scripted{ReplaySource: binding.RolloutSource{
				Replay: func(context.Context, string, map[string]bool) (observation.Source, error) {
					if boundary == "decoded rollout" {
						cancel()
					}
					return observation.Source{Thread: "main", Events: []observation.Event{{
						At: start.Add(time.Second), Started: start, Kind: observation.ItemCompleted,
						ThreadID: "main", TurnID: "turn", ItemID: "answer",
						Item: observation.Item{ID: "answer", Type: "agentMessage", Text: "Retained answer."},
					}}}, nil
				},
				Identity: func(string) (string, error) { return "", errors.New("unexpected sibling read") },
			}}
			history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
			var slots module.Slots
			if boundary == "optional timing" {
				slots.ReplayTiming = []replaytiming.Reader{func(context.Context, string, map[string]bool, func(replaytiming.Interval) error) error {
					cancel()
					return nil
				}}
			}
			service := moduletest.New(t, sessionreplay.Module, sessionreplay.Deps{History: history}, slots, script)
			source, err := service.Read(ctx, playback.ReadOptions{Path: "scripted-rollout", DebugDirectory: "scripted-timing"})
			if source != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation at %s returned source=%v error=%v", boundary, source, err)
			}
		})
	}
}
