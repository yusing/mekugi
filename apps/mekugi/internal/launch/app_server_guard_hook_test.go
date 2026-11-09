package launch

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/testsupport/uisnapshot"

	"github.com/yusing/mekugi/backends/codex/shellhook"
)

func TestUISnapshotGuardHookEscapeBeforeStart(t *testing.T) {
	for _, oneoff := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("oneoff=%t/failed=%t", oneoff, failed), func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.guardHookCheck.hash = "guard"
				thread := "main"
				if oneoff {
					thread = "oneoff"
					u.Turn = "main-turn"
					appServerTestKeys(t, u, "/oneoff task\r")
					fork := btwTestRequest(t, wire, "thread/fork", "main")
					btwTestReply(t, u, fork, `{"thread":{"id":"oneoff"}}`)
				} else {
					appServerTestKeys(t, u, "task\r")
				}
				check := appServerOneRequest(t, wire, "hooks/list", "")
				appServerTestKeys(t, u, "\x1b")
				ageTerminalEscape(terminalTest(u))
				if err := terminalTest(u).flushEscape(); err != nil {
					t.Fatal(err)
				}
				rows, _ := u.mainFrame(80, 20, 0)
				if oneoff {
					rows = u.sideFrame(80, 20)
				}
				frame := ansi.Strip(strings.Join(rows, "\n"))
				if wire.Len() != 0 || strings.TrimSpace(u.Text) != "task" || strings.Contains(frame, "approval guard") {
					t.Fatalf("canceled input retained guard: draft=%q wire=%s frame=\n%s", u.Text, wire.String(), frame)
				}
				if !failed {
					t.Log(frame)
					uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/guard-interrupt-oneoff-%t.txt", oneoff), frame+"\n")
				}
				result := fmt.Sprintf(`"result":%s`, guardHookTestResult(t, u.attached.cwd, guardHookTestEntry("guard")))
				notice, alert := u.attached.feedback.Text, u.attached.feedback.Alert
				if failed {
					result = `"error":{"code":-1,"message":"unavailable"}`
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, check.ID, result))
				if wire.Len() != 0 || u.guardHookCheck.readyThread != "" || u.attached.feedback.Text != notice || u.attached.feedback.Alert != alert {
					t.Fatalf("canceled check affected later work: ready=%q notice=%q wire=%s", u.guardHookCheck.readyThread, u.attached.feedback.Text, wire.String())
				}
				appServerTestKeys(t, u, "\r")
				check = appServerOneRequest(t, wire, "hooks/list", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, check.ID, guardHookTestResult(t, u.attached.cwd, guardHookTestEntry("guard"))))
				requests := appServerTurnRequests(t, wire)
				if len(requests) != 1 || requests[0].Method != "turn/start" || !strings.Contains(requests[0].text(), "task") {
					t.Fatalf("guarded retry did not start: %+v", requests)
				}
				start := requests[0]
				if start.Params.ThreadID != thread || oneoff && u.Turn != "main-turn" {
					t.Fatalf("guarded retry changed identity: %+v main=%q", start.Params, u.Turn)
				}
			})
		}
	}
}

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

func TestAppServerGuardHookEscapeRunningTurn(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.guardHookCheck.hash = "guard"
	appServerTestTurn(t, u, "running")
	appServerTestKeys(t, u, "\x1b")
	ageTerminalEscape(terminalTest(u))
	if err := terminalTest(u).flushEscape(); err != nil {
		t.Fatal(err)
	}
	interrupt := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "running", "interrupted")
	rows, _ := u.mainFrame(80, 20, 0)
	frame := ansi.Strip(strings.Join(rows, "\n"))
	if wire.Len() != 0 || !strings.Contains(frame, "Interrupted") || strings.Contains(frame, "approval guard") {
		t.Fatalf("interruption started a guard check: wire=%s frame=\n%s", wire.String(), frame)
	}
	appServerTestKeys(t, u, "next\r")
	appServerOneRequest(t, wire, "hooks/list", "")
}

func TestAppServerGuardHookChecksBeforeEachNewTurn(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.cwd = "/selected/workspace"
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
	if len(requests) != 1 || requests[0].Method != "hooks/list" || len(requests[0].Params.Cwds) != 1 || requests[0].Params.Cwds[0] != u.attached.cwd || len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) != 2 {
		t.Fatalf("pending input bypassed workspace check: requests=%+v unsent=%+v queued=%+v", requests, u.attached.conversation.Unsent, u.attached.conversation.Queued)
	}
	result := guardHookTestResult(t, u.attached.cwd, guardHookTestEntry("guard"))
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
	other := guardHookTestEntry("other")
	other["key"], other["matcher"] = "other", `^[A-Z&&[^A]]ash$`
	unverifiable := guardHookTestResult(t, "/workspace", guardHookTestEntry("guard"), other)
	for _, response := range []string{`"result":{"data":[]}`, `"error":{"code":-1,"message":"offline"}`, `"result":` + string(unverifiable)} {
		u, wire := newAppServerTestUI()
		u.attached.cwd, u.guardHookCheck.hash = "/workspace", "guard"
		appServerTestKeys(t, u, "first\rsecond\rtyping")
		request := appServerOneRequest(t, wire, "hooks/list", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, request.ID, response))
		if wire.Len() != 0 || u.Text != "first\nsecond\ntyping" || len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) != 0 || !u.attached.feedback.Alert || u.attached.feedback.Text == "" {
			t.Fatalf("failure lost drafts or submitted input: draft=%q unsent=%+v queued=%+v notice=%q wire=%q", u.Text, u.attached.conversation.Unsent, u.attached.conversation.Queued, u.attached.feedback.Text, wire.String())
		}
		appServerTestKeys(t, u, "\r")
		appServerOneRequest(t, wire, "hooks/list", "")
	}
}

func TestAppServerGuardHookIgnoresStaleThreadResponse(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.cwd, u.guardHookCheck.hash = "/old", "guard"
	appServerTestKeys(t, u, "old input\r")
	old := appServerOneRequest(t, wire, "hooks/list", "")
	// Thread switching owns pending-input removal. The outstanding host request
	// can still arrive after the selected thread and workspace change.
	u.Thread, u.attached.cwd, u.attached.conversation.Unsent, u.attached.conversation.Queued = "new", "/new", nil, nil
	appServerTestKeys(t, u, "new input\r")
	if wire.Len() != 0 {
		t.Fatalf("overlapping hook check: %q", wire.String())
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, old.ID, guardHookTestResult(t, "/old", guardHookTestEntry("guard"))))
	appServerOneRequest(t, wire, "hooks/list", "")
	if u.guardHookCheck.readyThread != "" || u.guardHookCheck.pendingThread != "new" || len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) != 1 || u.attached.presentation.Status.Alert {
		t.Fatalf("stale result affected new thread: check=%+v notice=%q", u.guardHookCheck, u.attached.feedback.Text)
	}
}
