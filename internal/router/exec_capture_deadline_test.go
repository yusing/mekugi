package router

import (
	"testing"
	"time"
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
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "rm missing", "workdir": workspace}))
	started := time.Now()
	streamNativeExecCommand(t, transform, "deadline-call", arguments)
	if elapsed := time.Since(started); elapsed > execCaptureHold+250*time.Millisecond {
		t.Fatalf("host forwarding exceeded capture hold: %v", elapsed)
	}
	observation := transform.local["deadline-call"].ExecObservation
	if observation == nil || observation.Reason != "capture deadline" || len(observation.Files) != 0 || len(observation.Omitted) == 0 {
		t.Fatalf("unavailable capture claimed a baseline: %+v", observation)
	}
}
