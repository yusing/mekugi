package sessioncontrol

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestQuestionResponseKeepsSecretOutOfPrompt(t *testing.T) {
	transport := new(requestTransport)
	q := &Question{Call: &prompt.Call{Questions: []prompt.Question{{ID: "secret", IsSecret: true, Done: true}}}, RequestID: jsontext.Value(`"original-request"`)}
	if err := q.Answer(NewRequests[struct{}](transport), map[int][]string{0: {"fixture-secret"}}); err != nil {
		t.Fatal(err)
	}
	if len(transport.responses) != 1 || string(transport.responses[0].id) != string(q.RequestID) {
		t.Fatal("question response lost its original host identity")
	}
	if len(q.Questions[0].Answer) != 0 || q.Sent || q.Resolved {
		t.Fatal("delivery changed the prompt or retained its secret")
	}
}

func TestPasswordDecisionKeepsOriginalGuard(t *testing.T) {
	guard := &ShellApprovalRequest{Reply: make(chan shellapproval.Reply, 1), Done: make(chan struct{})}
	q := &Question{Call: &prompt.Call{Questions: []prompt.Question{{IsSecret: true, Done: true}}}, Sudo: guard}
	outcome, delivered, err := q.AnswerPassword(map[int][]string{0: {"fixture-password"}})
	if err != nil || !delivered || outcome != "Password supplied" || !q.Sent {
		t.Fatalf("password decision: outcome=%q delivered=%v err=%v", outcome, delivered, err)
	}
	reply := <-guard.Reply
	if !reply.OK || reply.Password != "fixture-password" || len(q.Questions[0].Answer) != 0 {
		t.Fatal("password delivery changed its value or retained it in the prompt")
	}
	if q.CancelPassword() {
		t.Fatal("answered authentication received a cancellation reply")
	}
}
