package sessioncontrol

import "testing"

func TestSideCompletionAndDelayedCleanup(t *testing.T) {
	side := Side{Thread: "side", Busy: true, Starting: true, Closed: true}
	if side.Release() != "" {
		t.Fatal("cleanup preceded start acknowledgement")
	}
	side.Turn = "turn"
	if !side.Complete("turn") {
		t.Fatal("matching completion rejected")
	}
	side.StartAcknowledged("turn")
	if side.Turn != "" || side.Release() != "thread/unsubscribe" || side.Release() != "" {
		t.Fatalf("late acknowledgement revived execution or repeated cleanup: %+v", side)
	}
	side = Side{Thread: "side", Turn: "turn", Busy: true, Closed: true}
	if side.Complete("other") || side.Release() != "turn/interrupt" || side.Release() != "" {
		t.Fatal("side target changed or interrupt duplicated")
	}
	side.InterruptAcknowledged()
	if side.Release() != "thread/unsubscribe" {
		t.Fatal("interruption did not finish cleanup")
	}
	ledger := new(Requests[*Side])
	ledger.TrackSide("side", &side)
	ledger.Retire("main")
	if ledger.Side("side") != &side {
		t.Fatal("Main replacement retired side correlation")
	}
	ledger.CloseSide("side")
	if ledger.HasSides() {
		t.Fatal("closed side correlation retained")
	}
}
