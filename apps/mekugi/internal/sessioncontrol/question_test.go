package sessioncontrol

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
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

func TestPasswordDecisionUsesNativeSecretResponse(t *testing.T) {
	transport := new(requestTransport)
	requests := NewRequests[struct{}](transport)
	q := &Question{
		Call:      &prompt.Call{Questions: []prompt.Question{{IsSecret: true, Done: true}}},
		RequestID: jsontext.Value(`"secret-req-1"`),
		Secret:    &binding.ShellGuardSecretParams{Kind: binding.SudoPassword, Argv: []string{"sudo", "id"}},
	}
	outcome, delivered, err := q.AnswerPassword(requests, map[int][]string{0: {"fixture-password"}})
	if err != nil || !delivered || outcome != "Password supplied" || !q.Sent {
		t.Fatalf("password decision: outcome=%q delivered=%v err=%v", outcome, delivered, err)
	}
	if len(transport.responses) != 1 || string(transport.responses[0].id) != `"secret-req-1"` {
		t.Fatalf("unexpected responses: %+v", transport.responses)
	}
	resp, ok := transport.responses[0].result.(binding.ShellGuardSecretResponse)
	if !ok || resp.Decision != "provide" || resp.Secret == nil || *resp.Secret != "fixture-password" || len(q.Questions[0].Answer) != 0 {
		t.Fatalf("password delivery changed its value or retained it in the prompt: %#v", resp)
	}
	if q.CancelPassword(requests) {
		t.Fatal("answered authentication received a cancellation reply")
	}
}
