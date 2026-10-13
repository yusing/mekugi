package router

import (
	"github.com/yusing/mekugi/internal/livediff"
	"testing"
	"time"
)

func TestUISnapshotOrchestrateExitGuard(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.clock = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.view.painter.Theme = livediff.DarkTheme
	u.model, u.reasoningEffort = "snapshot-model", "high"
	u.orchestrateRequests = map[string]orchestrateRPC{"pending": {}}
	if quit, err := u.key(3); quit || err != nil {
		t.Fatal("departure guard failed", err)
	}
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "orchestrate-exit-guard", rows)
}

func TestUISnapshotOrchestrateQuitConfirmation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.clock = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.view.painter.Theme = livediff.DarkTheme
	u.model, u.reasoningEffort = "snapshot-model", "high"
	u.orchestrateRequests = map[string]orchestrateRPC{"pending": {}}
	u.draft = "/quit"
	if quit, err := u.key('\r'); quit || err != nil || !u.quitConfirmation {
		t.Fatal("exit skipped confirmation", err)
	}
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "orchestrate-quit-confirmation", rows)
}
