package sessioncontrol

import (
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

func TestHookTrustInventory(t *testing.T) {
	var hooks []any
	for _, status := range []string{"untrusted", "modified", "trusted", "managed"} {
		hooks = append(hooks, map[string]any{"key": status, "currentHash": "opaque/" + status, "trustStatus": status, "sourcePath": "/work/.codex/hooks.json", "eventName": "preToolUse", "handlerType": "mcpTool", "server": "tools", "tool": "check", "matcher": "Bash", "enabled": false})
	}
	data, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": "/work", "hooks": hooks}, map[string]any{"cwd": "/other", "hooks": hooks}}})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := HookTrustApprovals(data, "/work")
	if err != nil || len(approvals) != 2 {
		t.Fatalf("review inventory: %+v %v", approvals, err)
	}
	for _, a := range approvals {
		if a.Hook.Hash != "opaque/"+a.Hook.Key || a.Subject != "mcpTool tools check" || a.Choices[a.Selected].Approve || a.DenialChoice() >= 0 {
			t.Fatalf("lost authority or safe initial choice: %+v", a)
		}
		if details := strings.Join(a.Details, "\n"); !strings.Contains(details, "Disabled") || !strings.Contains(details, "Source: .codex/hooks.json") || !strings.Contains(details, "Matcher: Bash") {
			t.Fatalf("missing review context: %s", details)
		}
	}
}

func TestHookTrustWriteFailureAndIdentity(t *testing.T) {
	transport := &requestTransport{id: "save", err: errors.New("closed")}
	requests := NewRequests[*Approval](transport)
	a := &Approval{Hook: &HookTrust{Key: `plugin."quoted"\path`, Hash: "declaration/content"}}
	if err := requests.TrustHook(a, a); err == nil || a.Hook.Saving || requests.Pending() {
		t.Fatal("failed dispatch retained pending save")
	}
	transport.err = nil
	if err := requests.TrustHook(a, a); err != nil {
		t.Fatal(err)
	}
	if err := requests.TrustHook(a, a); err != nil || len(transport.sends) != 2 {
		t.Fatal("repeated decision duplicated write")
	}
	write := transport.sends[1].params.(map[string]any)
	edit := write["edits"].([]any)[0].(map[string]any)
	state := edit["value"].(map[string]any)
	if len(state) != 1 || state[a.Hook.Key].(map[string]any)["trusted_hash"] != a.Hook.Hash || edit["keyPath"] != "hooks.state" || edit["mergeStrategy"] != "upsert" || write["reloadUserConfig"] != true {
		t.Fatalf("lost opaque identity or merge semantics: %+v", write)
	}
	if request, ok := requests.Take("save"); !ok || request.Owner != a || request.Method != "config/batchWrite" {
		t.Fatal("write response lost original prompt authority")
	}
}

func TestHookTrustBlocksExecutionNotApprovalTransport(t *testing.T) {
	client := Client[string]{Live: Live{Thread: "main", HookReview: true}, Requests: NewRequests[string](&requestTransport{})}
	if client.AcceptsInput() || client.AcceptsShell() || client.AcceptsSettings() || !client.SideForkBlocked() || !client.Busy(false) || client.Calls() != nil {
		t.Fatal("hook review allowed execution before consent")
	}
	client.HookReview = false
	if !client.AcceptsInput() || !client.AcceptsShell() || !client.AcceptsSettings() || client.SideForkBlocked() || client.Busy(false) {
		t.Fatal("settled trust left execution blocked")
	}
}
