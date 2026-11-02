package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestApprovalDecisionUsesOriginalAuthority(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered", true: "failed"}[failed], func(t *testing.T) {
			transport := &requestTransport{}
			if failed {
				transport.err = errors.New("delivery failed")
			}
			requests := NewRequests[struct{}](transport)
			observations := new(Observations)
			response := map[string]any{"decision": "decline"}
			a := &Approval{
				Approval:  &prompt.Approval{Thread: "original-thread", Turn: "original-turn", Subject: "git push"},
				RequestID: jsontext.Value(`"host-approval"`), Responses: []map[string]any{response},
			}
			outcome, delivered, err := a.Decide(prompt.Choice{ID: 0, Deny: true, Outcome: "Denied"}, " wait ", ApprovalGrants{}, requests, observations)
			if !errors.Is(err, transport.err) || delivered == failed || requests.Pending() {
				t.Fatalf("decision: delivered=%v err=%v pending=%v", delivered, err, requests.Pending())
			}
			if len(transport.responses) != 1 || string(transport.responses[0].id) != string(a.RequestID) || !reflect.DeepEqual(transport.responses[0].result, response) {
				t.Fatalf("changed response authority: %+v", transport.responses)
			}
			wantFeedback := "Command: git push\nuser denied this command with a reason: wait"
			if failed {
				wantFeedback = ""
			} else if outcome != "Denied: wait" {
				t.Fatalf("outcome=%q", outcome)
			}
			if got := observations.Feedback(a.Thread, a.Turn); got != wantFeedback || observations.Feedback("replacement", a.Turn) != "" {
				t.Fatalf("feedback=%q, want %q", got, wantFeedback)
			}
		})
	}
}

func TestApprovalGrantsRegisteredKindsAndAttachmentIsolation(t *testing.T) {
	guards := []binding.ShellApproval{{Kind: binding.RemoteWrite}, {Kind: binding.Sudo}}
	grants := NewApprovalGrants(guards)
	other := NewApprovalGrants(guards)
	for _, kind := range []string{"", "sudo"} {
		request := &ShellApprovalRequest{Kind: kind, Cwd: "/workspace", Executable: "/tools/command", Argv: []string{"printf", "a b"}, Reply: make(chan shellapproval.Reply, 1)}
		if !grants.Available(request) || grants.Allows(request) {
			t.Fatal("registered guard has incorrect initial grants")
		}
		approval := &Approval{Guard: request}
		if _, delivered, err := approval.Decide(prompt.Choice{Approve: true, Session: true}, "", grants, nil, nil); err != nil || !delivered || !(<-request.Reply).OK {
			t.Fatalf("session approval: delivered=%v err=%v", delivered, err)
		}
		if !grants.Allows(request) || other.Allows(request) || NewApprovalGrants(guards).Allows(request) {
			t.Fatal("grant lost exact command or escaped attachment lifetime")
		}
		for _, changed := range []ShellApprovalRequest{
			{Kind: kind, Cwd: "/other", Executable: request.Executable, Argv: request.Argv},
			{Kind: kind, Cwd: request.Cwd, Executable: "/other/command", Argv: request.Argv},
			{Kind: kind, Cwd: request.Cwd, Executable: request.Executable, Argv: []string{"printf", "a", "b"}},
		} {
			if grants.Allows(&changed) {
				t.Fatalf("authorized changed identity: %+v", changed)
			}
		}
		// A module's grant never authorizes another kind, even for identical argv.
		changed := *request
		if kind == "" {
			changed.Kind = "sudo"
		} else {
			changed.Kind = "unknown"
		}
		if grants.Allows(&changed) {
			t.Fatal("grant crossed guard kinds")
		}
	}
	for _, kind := range []string{"sudo", "unknown"} {
		request := &ShellApprovalRequest{Kind: kind}
		if NewApprovalGrants(guards[:1]).Available(request) || NewApprovalGrants(nil).Available(request) || (ApprovalGrants{}).Allows(request) {
			t.Fatal("absent guard acquired authority")
		}
	}
}
