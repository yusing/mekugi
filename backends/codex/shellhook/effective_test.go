package shellhook

import (
	json "encoding/json/v2"
	"testing"
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
	return map[string]any{"key": Key, "eventName": "preToolUse", "handlerType": "command", "currentHash": hash, "matcher": "^Bash$", "trustStatus": "trusted", "enabled": true, "async": false}
}

func TestValidateGuardHookEffectiveConfiguration(t *testing.T) {
	const hash = "sha256:expected"
	for _, tt := range []struct {
		name, field string
		value       any
	}{
		{"disabled", "enabled", false}, {"untrusted", "trustStatus", "untrusted"},
		{"wrong key", "key", "/user/config.toml:pre_tool_use:0:0"},
		{"wrong hash", "currentHash", hash + " extra"}, {"wrong type", "handlerType", "mcpTool"}, {"wrong matcher", "matcher", ".*"},
		{"asynchronous", "async", true}, {"wrong event", "eventName", "postToolUse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hook := guardHookTestEntry(hash)
			hook[tt.field] = tt.value
			if err := Validate(guardHookTestResult(t, "/workspace", hook), "/workspace", hash); err == nil {
				t.Fatal("accepted ineffective guard")
			}
		})
	}
	for _, trust := range []string{"trusted", "managed"} {
		hook := guardHookTestEntry(hash)
		hook["trustStatus"] = trust
		if err := Validate(guardHookTestResult(t, "/workspace", hook), "/workspace", hash); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{[]byte(`{`), []byte(`{"data":[]}`), guardHookTestResult(t, "/other", guardHookTestEntry(hash)), []byte(`{"data":[{"cwd":"/workspace","errors":["cannot load hooks"]}]}`)} {
		if err := Validate(data, "/workspace", hash); err == nil {
			t.Fatalf("accepted unverifiable host result: %s", data)
		}
	}
}

func TestValidateGuardHookCompetingShellHooks(t *testing.T) {
	const hash = "guard"
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
			other := map[string]any{"key": "other", "handlerType": "command", "command": "other-command", "eventName": tt.event, "matcher": tt.matcher, "trustStatus": tt.trust, "enabled": tt.enabled, "async": tt.async}
			err := Validate(guardHookTestResult(t, "/workspace", guardHookTestEntry(hash), other), "/workspace", hash)
			if (err != nil) != tt.conflict {
				t.Fatalf("conflict=%v error=%v", tt.conflict, err)
			}
		})
	}
}
