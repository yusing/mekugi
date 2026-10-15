package router

import (
	json "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/backends/codex/shellhook"
)

func guardHookTestResult(t *testing.T, cwd string, hooks ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "errors": []any{}, "hooks": hooks}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func guardHookTestEntry(hash string) map[string]any {
	return map[string]any{"key": shellhook.Key, "eventName": "preToolUse", "handlerType": "command", "currentHash": hash, "matcher": "^Bash$", "trustStatus": "trusted", "enabled": true, "async": false}
}

func TestAppServerGuardHookChecksBeforeEachNewTurn(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/selected/workspace"
	u.guardHookCheck.hash = "guard"
	appServerTestKeys(t, u, "first\rsecond\r")
	type hookRequest struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Cwds []string `json:"cwds"`
		} `json:"params"`
	}
	requests := appServerDrainRequests[hookRequest](t, wire)
	if len(requests) != 1 || requests[0].Method != "hooks/list" || len(requests[0].Params.Cwds) != 1 || requests[0].Params.Cwds[0] != u.session.cwd || len(u.unsent)+len(u.queued) != 2 {
		t.Fatalf("pending input bypassed workspace check: requests=%+v unsent=%+v queued=%+v", requests, u.unsent, u.queued)
	}
	result := guardHookTestResult(t, u.session.cwd, guardHookTestEntry("guard"))
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, requests[0].ID, result))
	start := appServerOneRequest(t, wire, "turn/start", "first\nsecond")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"t"}}}`, start.ID))
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "steer\r")
	steer := appServerOneRequest(t, wire, "turn/steer", "steer")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
	appServerTestTurnEnd(t, u, "t", "completed")
	appServerTestKeys(t, u, "next\r")
	appServerOneRequest(t, wire, "hooks/list", "")
}

func TestAppServerGuardHookFailureRestoresDrafts(t *testing.T) {
	for _, response := range []string{`"result":{"data":[]}`, `"error":{"code":-1,"message":"offline"}`} {
		u, wire := newAppServerTestUI()
		u.session.cwd, u.guardHookCheck.hash = "/workspace", "guard"
		appServerTestKeys(t, u, "first\rsecond\rtyping")
		request := appServerOneRequest(t, wire, "hooks/list", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, request.ID, response))
		if wire.Len() != 0 || u.draft != "first\nsecond\ntyping" || len(u.unsent)+len(u.queued) != 0 || !u.noticeAlert || u.notice == "" {
			t.Fatalf("failure lost drafts or submitted input: draft=%q unsent=%+v queued=%+v notice=%q wire=%q", u.draft, u.unsent, u.queued, u.notice, wire.String())
		}
		appServerTestKeys(t, u, "\r")
		appServerOneRequest(t, wire, "hooks/list", "")
	}
}

func TestAppServerGuardHookIgnoresStaleThreadResponse(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd, u.guardHookCheck.hash = "/old", "guard"
	appServerTestKeys(t, u, "old input\r")
	old := appServerOneRequest(t, wire, "hooks/list", "")
	// Thread switching owns pending-input removal. The outstanding host request
	// can still arrive after the selected thread and workspace change.
	u.thread, u.session.cwd, u.unsent, u.queued = "new", "/new", nil, nil
	appServerTestKeys(t, u, "new input\r")
	if wire.Len() != 0 {
		t.Fatalf("overlapping hook check: %q", wire.String())
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, old.ID, guardHookTestResult(t, "/old", guardHookTestEntry("guard"))))
	appServerOneRequest(t, wire, "hooks/list", "")
	if u.guardHookCheck.readyThread != "" || u.guardHookCheck.pendingThread != "new" || len(u.unsent)+len(u.queued) != 1 || u.alert {
		t.Fatalf("stale result affected new thread: check=%+v notice=%q", u.guardHookCheck, u.notice)
	}
}
