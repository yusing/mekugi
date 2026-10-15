package router

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestNativeExecForwardingWhileCaptureWorkersUnavailable(t *testing.T) {
	// Occupied slots model filesystem workers that have not returned. The
	// consuming SSE boundary must still forward the unchanged host call.
	for range cap(execCaptureSlots) {
		execCaptureSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(execCaptureSlots) {
			<-execCaptureSlots
		}
	})
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "deadline-session")
	arguments := string(responses.MustMarshalJSON(map[string]any{"cmd": "rm missing", "workdir": workspace}))
	started := time.Now()
	retainCommandObservation(t, transform, "deadline-call", arguments)
	if elapsed := time.Since(started); elapsed > execCaptureHold+250*time.Millisecond {
		t.Fatalf("host forwarding exceeded capture hold: %v", elapsed)
	}
	observation := transform.local["deadline-call"].ExecObservation
	if observation == nil || observation.Reason != "capture deadline" || len(observation.Files) != 0 || len(observation.Omitted) != 0 {
		t.Fatalf("unavailable capture claimed a baseline: %+v", observation)
	}
	unrelated := filepath.Join(workspace, "unrelated.txt")
	writeTestFile(t, unrelated, "written while capture was unavailable\n")
	reviews, _, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	for _, review := range reviews {
		if review.BeforePath == unrelated || review.AfterPath == unrelated {
			t.Fatalf("capture deadline fabricated a workspace-wide edit: %+v", reviews)
		}
	}
}
