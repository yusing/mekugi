package router

import (
	json "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/internal/vcsguard"
)

func guardHookTestResult(t *testing.T, cwd string, hooks ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": cwd, "errors": []any{}, "hooks": hooks}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func guardHookTestEntry(command string) map[string]any {
	return map[string]any{"key": vcsguard.HookKey, "eventName": "preToolUse", "command": command, "matcher": "^Bash$", "trustStatus": "trusted", "enabled": true, "async": false}
}

func TestValidateGuardHookEffectiveConfiguration(t *testing.T) {
	const command = "'/helper' --vcs-hook '/guard'"
	for _, tt := range []struct {
		name, field string
		value       any
	}{
		{"disabled", "enabled", false}, {"untrusted", "trustStatus", "untrusted"},
		{"wrong key", "key", "/user/config.toml:pre_tool_use:0:0"},
		{"wrong command", "command", command + " extra"}, {"wrong matcher", "matcher", ".*"},
		{"asynchronous", "async", true}, {"wrong event", "eventName", "postToolUse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hook := guardHookTestEntry(command)
			hook[tt.field] = tt.value
			if err := validateGuardHook(guardHookTestResult(t, "/workspace", hook), "/workspace", command); err == nil {
				t.Fatal("accepted ineffective guard")
			}
		})
	}
	for _, trust := range []string{"trusted", "managed"} {
		hook := guardHookTestEntry(command)
		hook["trustStatus"] = trust
		if err := validateGuardHook(guardHookTestResult(t, "/workspace", hook), "/workspace", command); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{[]byte(`{`), []byte(`{"data":[]}`), guardHookTestResult(t, "/other", guardHookTestEntry(command)), []byte(`{"data":[{"cwd":"/workspace","errors":["cannot load hooks"]}]}`)} {
		if err := validateGuardHook(data, "/workspace", command); err == nil {
			t.Fatalf("accepted unverifiable host result: %s", data)
		}
	}
}

func TestValidateGuardHookCompetingShellHooks(t *testing.T) {
	const command = "guard"
	for _, tt := range []struct {
		name, matcher, trust, event string
		enabled, async, conflict    bool
	}{
		{"Bash rewrite", "^Bash$", "trusted", "preToolUse", true, false, true},
		{"exec rewrite", "^exec_command$", "managed", "preToolUse", true, false, true},
		{"shell rewrite", "^shell(_command)?$", "trusted", "preToolUse", true, false, true},
		{"all tools rewrite", "", "trusted", "preToolUse", true, false, true},
		{"wildcard", "*", "trusted", "preToolUse", true, false, true},
		{"exact alternatives", "Read|Bash", "trusted", "preToolUse", true, false, true},
		{"plain prefix is not a match", "B", "trusted", "preToolUse", true, false, false},
		{"plain alternatives are not regex", "Read|Bas", "trusted", "preToolUse", true, false, false},
		{"invalid matcher", "[", "trusted", "preToolUse", true, false, true},
		{"disabled", "^Bash$", "trusted", "preToolUse", false, false, false},
		{"untrusted", "^Bash$", "untrusted", "preToolUse", true, false, false},
		{"async observer", "^Bash$", "trusted", "preToolUse", true, true, false},
		{"read hook", "^Read$", "trusted", "preToolUse", true, false, false},
		{"recovery hook", "^compact$", "trusted", "sessionStart", true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := map[string]any{"key": "other", "command": "other-command", "eventName": tt.event, "matcher": tt.matcher, "trustStatus": tt.trust, "enabled": tt.enabled, "async": tt.async}
			err := validateGuardHook(guardHookTestResult(t, "/workspace", guardHookTestEntry(command), other), "/workspace", command)
			if (err != nil) != tt.conflict {
				t.Fatalf("conflict=%v error=%v", tt.conflict, err)
			}
		})
	}
}

func TestAppServerGuardHookChecksBeforeEachNewTurn(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/selected/workspace"
	u.guardHookCheck.command = "guard"
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
		u.session.cwd, u.guardHookCheck.command = "/workspace", "guard"
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
	u.session.cwd, u.guardHookCheck.command = "/old", "guard"
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
