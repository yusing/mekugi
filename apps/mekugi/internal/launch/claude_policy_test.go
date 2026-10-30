package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestClaudeLaunchPolicyRejectionNotDispatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b := newClaudeHookFixture(t).bridge
	hub := sessionapi.NewHub(ctx, &claudeNativeLauncher{parent: &claudeNativeParent{}})
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(ctx, server.Client(), "ws"+strings.TrimPrefix(server.URL, "http")+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	start, err := json.Marshal(sessionapi.Start{Workspace: b.settings.Workspace, Model: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		ID string `json:"id"`
	}
	if err := rpc.Call(ctx, "session/start", sessionapi.RPCParams{Body: start}, &ready); err != nil || ready.ID != "parent" {
		t.Fatalf("root readiness: %+v %v", ready, err)
	}
	b.execution.hub, b.execution.setup, b.execution.windows = hub, &threadSetup{}, b.windows
	settings := b.settings
	settings.ApprovalsReviewer = "auto_review"
	receipt, err := b.execution.createClaude(ctx, "parent", "", settings)
	dispatch, ok := errors.AsType[*binding.DispatchError](err)
	if !ok || dispatch.Outcome != binding.NotDispatched || receipt.Uncertain {
		t.Fatalf("pre-start policy refusal lost definite outcome: receipt=%+v err=%v", receipt, err)
	}
	if !strings.Contains(err.Error(), `does not support reviewer "auto_review"`) {
		t.Fatalf("creation failed outside captured policy admission: %v", err)
	}
}

func TestClaudeGranularPromptRejectsWithoutCategory(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	b.execution.approvals = sessioncontrol.NewDelegateApprovals(t.Context())
	t.Cleanup(b.execution.approvals.Close)
	for _, raw := range []string{
		`{"granular":{"sandbox_approval":true,"rules":true,"skill_approval":true,"request_permissions":true,"mcp_elicitations":true}}`,
		`{"granular":{"sandbox_approval":false,"rules":false,"mcp_elicitations":false}}`,
	} {
		var err error
		b.policy, err = claude.CompilePolicy(jsontext.Value(raw), "user", false)
		if err != nil {
			t.Fatal(err)
		}
		result, err := b.request(t.Context(), "permission", jsontext.Value(`{"name":"Bash","toolUseID":"granular","arguments":{"command":"echo approved"}}`))
		if err != nil {
			t.Fatal(err)
		}
		var decision struct{ Behavior, Message string }
		if err := json.Unmarshal(result, &decision, json.MatchCaseInsensitiveNames(true)); err != nil || decision.Behavior != "deny" || !strings.Contains(decision.Message, "cannot classify") {
			t.Fatalf("uncategorized native approval: %s, %v", result, err)
		}
		select {
		case <-b.execution.approvals.Requests():
			t.Fatal("granular prompt reached the human approval dock")
		default:
		}
	}
}

func TestClaudeRoleShellReductionCoversShellTools(t *testing.T) {
	f := newClaudeHookFixture(t)
	f.bridge.settings.Config = jsontext.Value(`{"features.shell_tool":false}`)
	for _, tool := range []string{"Bash", "PowerShell", "Monitor"} {
		result := f.hook(t, "PreToolUse", tool, "reduced-"+tool, jsontext.Value(`{"description":"watch","command":"printf denied > denied.txt"}`), nil)
		if !strings.Contains(string(result), `"permissionDecision":"deny"`) || !strings.Contains(string(result), "Shell tools are disabled") {
			t.Fatalf("%s bypassed the captured role shell reduction: %s", tool, result)
		}
	}
}
