package launch

import (
	"context"
	"testing"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/modules/sessionreplay"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func replayTestService(t *testing.T, diagnostics *transportcapture.Service) *playback.Service {
	t.Helper()
	slots := module.Slots{}
	if diagnostics != nil {
		slots.ReplayTiming = []replaytiming.Reader{diagnostics.ReplayTiming}
	}
	service, err := sessionreplay.Module.New(sessionreplay.Deps{History: testHistory()}, slots)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionreplay.Module.Bindings.Register(new(binding.Registry), service); err != nil {
		t.Fatal(err)
	}
	return service
}
func replayReadTest(t *testing.T, diagnostics *transportcapture.Service, ctx context.Context, path, debug string, seed uint64) (*playback.Source, error) {
	t.Helper()
	directory, _ := history.ReplayDirectory()
	return replayTestService(t, diagnostics).Read(ctx, playback.ReadOptions{Path: path, HistoryDirectory: directory, DebugDirectory: debug, Seed: seed})
}
