package executionobservation

import (
	"testing"

	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestSavedReviewSwitchRetiresOnlyPreviewAttachment(t *testing.T) {
	review := sessionreview.New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	defer review.Close()
	review.IncludeThread("/workspace", "main", true)
	old := review.Preview()
	old.Enable()
	frame := previewViewFixture("old", 1)
	frame.Thread = "main"
	old.Update(frame, false)
	if len(old.Current()) != 1 {
		t.Fatal("preview fixture was not admitted")
	}
	worker := startLiveDiffPreview(t.Context(), old, "/workspace", "main")
	windows := new(Windows)
	windows.open(&execWindow{ref: "host-cell", thread: "main", turn: "turn"})
	review.ResetScope()
	review.IncludeThread("/workspace", "next", true)
	select {
	case <-worker.Done():
	case <-time.After(time.Second):
		t.Fatal("retired attachment kept its input projection worker")
	}
	next := review.Preview()
	if next == old || old.Context().Err() == nil || next.Context().Err() != nil || len(next.Current()) != 0 || !next.Enabled() {
		t.Fatal("session switch retained old preview ownership")
	}
	if window := windows.find("host-cell"); window == nil || !window.closed.IsZero() || t.Context().Err() != nil {
		t.Fatal("preview retirement finalized capture or canceled host authority")
	}
}
