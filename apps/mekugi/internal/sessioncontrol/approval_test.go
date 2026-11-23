package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
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

func TestShellGuardApprovalGrantsAndNativeResponse(t *testing.T) {
	for _, kind := range []binding.ShellGuardKind{binding.RemoteWrite, binding.Sudo} {
		grants := NewApprovalGrants()
		other := NewApprovalGrants()
		transport := &requestTransport{}
		requests := NewRequests[struct{}](transport)
		observations := new(Observations)
		request := &binding.ShellGuardApprovalParams{
			Kind:       kind,
			Cwd:        "/workspace",
			Executable: "/tools/command",
			Argv:       []string{"printf", "a b"},
		}
		if grants.Allows(request) {
			t.Fatal("fresh grants authorized command")
		}
		approval := &Approval{
			Approval:   &prompt.Approval{Thread: "t1", Turn: "turn1", Subject: "printf 'a b'"},
			RequestID:  jsontext.Value(`"sg-1"`),
			ShellGuard: request,
		}
		transport.err = errors.New("disconnected")
		if _, delivered, err := approval.Decide(prompt.Choice{Approve: true, Session: true}, "", grants, requests, observations); err == nil || delivered || grants.Allows(request) {
			t.Fatal("failed delivery installed a session grant")
		}
		transport.err, transport.responses = nil, nil
		if _, delivered, err := approval.Decide(prompt.Choice{Approve: true, Session: true}, "", grants, requests, observations); err != nil || !delivered {
			t.Fatalf("session approval: delivered=%v err=%v", delivered, err)
		}
		if len(transport.responses) != 1 || string(transport.responses[0].id) != `"sg-1"` {
			t.Fatalf("unexpected transport responses: %+v", transport.responses)
		}
		gotResp, ok := transport.responses[0].result.(binding.ShellGuardApprovalResponse)
		if !ok || gotResp.Decision != "acceptForSession" {
			t.Fatalf("response=%#v", transport.responses[0].result)
		}
		if !grants.Allows(request) || other.Allows(request) || NewApprovalGrants().Allows(request) {
			t.Fatal("grant lost exact command or escaped attachment lifetime")
		}
		for _, changed := range []binding.ShellGuardApprovalParams{
			{Kind: kind, Cwd: "/other", Executable: request.Executable, Argv: request.Argv},
			{Kind: kind, Cwd: request.Cwd, Executable: "/other/command", Argv: request.Argv},
			{Kind: kind, Cwd: request.Cwd, Executable: request.Executable, Argv: []string{"printf", "a", "b"}},
		} {
			if grants.Allows(&changed) {
				t.Fatalf("authorized changed identity: %+v", changed)
			}
		}
		changed := *request
		if kind == binding.RemoteWrite {
			changed.Kind = binding.Sudo
		} else {
			changed.Kind = binding.RemoteWrite
		}
		if grants.Allows(&changed) {
			t.Fatal("grant crossed guard kinds")
		}
		// Denial carries reason directly in native response without duplicating proxy observations.
		denyTransport := &requestTransport{}
		denyRequests := NewRequests[struct{}](denyTransport)
		denyApproval := &Approval{
			Approval:   &prompt.Approval{Thread: "t1", Turn: "turn1", Subject: "git push"},
			RequestID:  jsontext.Value(`"sg-2"`),
			ShellGuard: request,
		}
		outcome, delivered, err := denyApproval.Decide(prompt.Choice{Deny: true, Outcome: "Denied"}, " not yet ", grants, denyRequests, observations)
		if err != nil || !delivered || outcome != "Denied: not yet" {
			t.Fatalf("deny: outcome=%q delivered=%v err=%v", outcome, delivered, err)
		}
		denyResp, ok := denyTransport.responses[0].result.(binding.ShellGuardApprovalResponse)
		if !ok || denyResp.Decision != "decline" || denyResp.Reason == nil || *denyResp.Reason != "user denied this command with a reason: not yet" {
			t.Fatalf("deny response=%#v", denyTransport.responses[0].result)
		}
		if got := observations.Feedback("t1", "turn1"); got != "" {
			t.Fatalf("native shell guard deny duplicated proxy feedback: %q", got)
		}
	}
}
