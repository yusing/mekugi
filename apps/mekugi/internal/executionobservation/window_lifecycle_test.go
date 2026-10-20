package executionobservation

import "testing"

func TestStopTurnPreviewsPreservesCapture(t *testing.T) {
	windows := new(Windows)
	canceled := false
	otherCanceled := false
	windows.open(&execWindow{ref: "main", thread: "main", turn: "turn", previewCancel: func() { canceled = true }})
	windows.open(&execWindow{ref: "child", thread: "child", turn: "turn", previewCancel: func() { otherCanceled = true }})
	windows.StopTurnPreviews("main", "turn")
	if !canceled || otherCanceled || !windows.find("main").closed.IsZero() || !windows.find("child").closed.IsZero() {
		t.Fatal("preview stop crossed owners or finalized capture")
	}
}
