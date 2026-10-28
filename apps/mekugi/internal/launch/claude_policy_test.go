package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/claude"
)

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
		b.policy, err = claude.CompilePolicy(jsontext.Value(raw), "user")
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
