package router

import "testing"

func TestAppServerReplacementRetiresOnlyMainRequests(t *testing.T) {
	u, wire := newAppServerTestUI()
	persistSessionTitle(&u.Client, u.titleGenerator, &u.attached.feedback, u.shell, u.clock, &u.dirty, sessionTitleUpdate{thread: "main", name: "Old title"})
	title := btwTestRequest(t, wire, "thread/name/set", "main")
	appServerTestKeys(t, u, "/btw question\r")
	side := btwTestRequest(t, wire, "thread/fork", "main")
	appServerTestKeys(t, u, "/status\r")
	status := btwTestRequest(t, wire, "account/read", "")
	if err := u.sessionCommand("/clear"); err != nil {
		t.Fatal(err)
	}
	clear := btwTestRequest(t, wire, "thread/start", "")
	btwTestReply(t, u, clear, `{"thread":{"id":"fresh","cwd":"/tmp","name":"Fresh title"}}`)
	appServerDrainRequests[btwTestRPC](t, wire)
	if u.Thread != "fresh" || u.side != nil || u.statusPanel != nil {
		t.Fatal("confirmed replacement retained old presentation")
	}

	// Main's stale response cannot resurrect a report or fetch usage. A title
	// save still settles its original owner without renaming the replacement.
	btwTestReply(t, u, status, `{"account":{"type":"chatgpt"}}`)
	btwTestReply(t, u, title, `{}`)
	if wire.Len() != 0 || u.title != "Fresh title" || !u.Names.AllowsGenerated("main") {
		t.Fatalf("late main responses affected replacement: title=%q wire=%s", u.title, wire.String())
	}
	// The in-flight side fork still needs its own cleanup. Retiring Main must
	// not lose the response that identifies the newly created side thread.
	btwTestReply(t, u, side, `{"thread":{"id":"old-side"}}`)
	unload := btwTestRequest(t, wire, "thread/unsubscribe", "old-side")
	btwTestReply(t, u, unload, `{}`)
	btwTestReply(t, u, side, `{"thread":{"id":"duplicate-side"}}`)
	if wire.Len() != 0 || u.side != nil || u.Thread != "fresh" {
		t.Fatalf("duplicate response revived side work: %s", wire.String())
	}
}
