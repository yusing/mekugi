package launch

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

// The test UI dispatches host notifications directly. The connection's reset
// receipt handler feeds this same manual-compaction owner in production.
func compactWaitReset(u *appServerUI, request appServerTurnRequest, turn, status string) {
	u.Compaction.ObserveReset(sessioncontrol.ContextReset{Thread: "main", RequestID: request.Params.ContextResetRequestID,
		Turn: turn, Status: status})
}

func compactTestStart(t *testing.T, u *appServerUI, request appServerTurnRequest, turn string) {
	t.Helper()
	compactWaitReset(u, request, turn, "accepted")
	appServerTestTurn(t, u, turn)
}

func compactTestEnd(t *testing.T, u *appServerUI, request appServerTurnRequest, turn, status string) {
	t.Helper()
	resetStatus := status
	if status == "interrupted" {
		resetStatus = "canceled"
	}
	compactWaitReset(u, request, turn, resetStatus)
	appServerTestTurnEnd(t, u, turn, status)
}

func compactTestCancelRequest(t *testing.T, wire *appServerTestInput, request appServerTurnRequest) appServerTurnRequest {
	t.Helper()
	cancel := appServerOneRequest(t, wire, "thread/compact/cancel", "")
	if request.Params.ContextResetRequestID == "" || cancel.Params.ContextResetRequestID != request.Params.ContextResetRequestID || cancel.Params.ThreadID != request.Params.ThreadID {
		t.Fatalf("cancellation did not target its manual reset request: start=%+v cancel=%+v", request, cancel)
	}
	return cancel
}
