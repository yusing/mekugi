package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
)

// Question retains response authority beside its existing Conversation record.
// Secrets are transient arguments, never control state or presentation values.
type Question struct {
	*prompt.Call
	RequestID jsontext.Value
	Sudo      *ShellApprovalRequest
}

func (q *Question) Answer(transport Responder, secrets map[int][]string) error {
	return transport.Respond(q.RequestID, conversationbinding.QuestionResponse(q.Call, secrets))
}

// AnswerPassword reports whether a still-live authentication request was answered.
// Source: apps/mekugi/internal/router/sudo.go:22:44@c8cec41a.
func (q *Question) AnswerPassword(answers map[int][]string) (outcome string, delivered bool, err error) {
	if q.Sudo.Finished() {
		return "", false, nil
	}
	question := &q.Questions[0]
	reply := shellapproval.Reply{Reason: "sudo authentication denied"}
	if question.Done && !question.Skipped && len(answers[0]) == 1 {
		password := answers[0][0]
		if !sudo.ValidPassword(password) {
			return "", false, errors.New("sudo password must be a single line")
		}
		reply.OK, reply.Password = true, password
	}
	q.Sent = true
	q.Sudo.Reply <- reply
	if reply.OK {
		return "Password supplied", true, nil
	}
	return "Denied", true, nil
}

func (q *Question) CancelPassword() bool {
	if q.Sent || q.Sudo.Finished() {
		return false
	}
	q.Sudo.Reply <- shellapproval.Reply{Reason: "sudo authentication canceled"}
	return true
}
