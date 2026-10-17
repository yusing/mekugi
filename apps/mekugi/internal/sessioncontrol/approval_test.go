package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
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
