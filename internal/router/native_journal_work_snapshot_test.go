package router

import "testing"

func TestUISnapshotHostJournalIOPending(t *testing.T) {
	u, _ := hostJournalIOFixture(t, "off", "")
	unblock := hostJournalIOHold(t, u, "state")
	defer unblock()
	u.turn = "first-turn"
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn","status":"completed"}}}`)
	assertNativeUISnapshot(t, "journal-host-io-pending", []string{u.journalResetStrip(100)})
	unblock()
	drainOrchestrateWork(t, u)
}
