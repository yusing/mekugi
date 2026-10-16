package capture

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis"
)

func TestCaptureWhileWorkersUnavailable(t *testing.T) {
	// Occupied slots model filesystem workers that have not returned. The
	// capture boundary must release the unchanged command for host forwarding.
	for range cap(execCaptureSlots) {
		execCaptureSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(execCaptureSlots) {
			<-execCaptureSlots
		}
	})
	workspace := t.TempDir()
	commands := []commandanalysis.Command{{Command: "rm missing", Workdir: workspace, Shell: "bash"}}
	started := time.Now()
	observation, observed := Capture(t.Context(), commands, false, false, Options{Directory: workspace})
	if elapsed := time.Since(started); elapsed > execCaptureHold+250*time.Millisecond {
		t.Fatalf("host forwarding exceeded capture hold: %v", elapsed)
	}
	if !observed || observation == nil || observation.Reason != "capture deadline" || len(observation.Files) != 0 || len(observation.Omitted) != 0 {
		t.Fatalf("unavailable capture claimed a baseline: %+v", observation)
	}
	unrelated := filepath.Join(workspace, "unrelated.txt")
	writeTestFile(t, unrelated, "written while capture was unavailable\n")
	result := Reconcile(t.Context(), *observation, Comparison{})
	reviews := result.Files
	for _, review := range reviews {
		if review.BeforePath == unrelated || review.AfterPath == unrelated {
			t.Fatalf("capture deadline fabricated a workspace-wide edit: %+v", reviews)
		}
	}
}
