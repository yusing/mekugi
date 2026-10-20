package executionobservation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func execTrackPath() string {
	entries := filepath.SplitList(os.Getenv("PATH"))
	entries = slices.DeleteFunc(entries, func(entry string) bool {
		return filepath.Base(entry) == shellinstrumentation.VCSDirectory
	})
	return strings.Join(entries, string(os.PathListSeparator))
}

// exec.Command resolves a bare name before cmd.Env is set. Resolve fixture
// shells from the isolated PATH instead, without changing process-wide state.
func execTrackShellExecutable(t *testing.T, name string) string {
	t.Helper()
	for _, directory := range filepath.SplitList(execTrackPath()) {
		candidate, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if path, err := exec.LookPath(candidate); err == nil {
			// These fixtures need the native interpreter. A mise shim can
			// redispatch through the fixture's guard and rewrite its script.
			if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(resolved) == "mise" {
				continue
			}
			return path
		}
	}
	t.Skipf("%s unavailable", name)
	return ""
}

func liveDiffTestBroker(t *testing.T, store *mekugiReplayStore, scope previewmodel.Scope) (*previewmodel.Session, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	broker := newLivePreview(ctx)
	setLiveScope(broker, scope)
	store.liveDiff = testReview(broker).Publish
	return broker, cancel
}
