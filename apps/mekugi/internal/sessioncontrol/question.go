package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// Question retains response authority beside its existing Conversation record.
// Secrets are transient arguments, never control state or presentation values.
type Question struct {
	*prompt.Call
	RequestID jsontext.Value
	Secret    *binding.ShellGuardSecretParams
}

func (q *Question) Answer(transport Responder, secrets map[int][]string) error {
	return transport.Respond(q.RequestID, conversationbinding.QuestionResponse(q.Call, secrets))
}

// AnswerPassword delivers one private secret prompt answer over JSON-RPC.
func (q *Question) AnswerPassword(transport Responder, answers map[int][]string) (outcome string, delivered bool, err error) {
	if q.Sent || q.Resolved {
		return "", false, nil
	}
	if err := q.ValidatePassword(answers); err != nil {
		return "", false, err
	}
	question := &q.Questions[0]
	var response binding.ShellGuardSecretResponse
	if question.Done && !question.Skipped && len(answers[0]) == 1 {
		password := answers[0][0]
		response = binding.ShellGuardSecretResponse{Decision: "provide", Secret: &password}
	} else {
		reason := "authentication denied"
		response = binding.ShellGuardSecretResponse{Decision: "decline", Reason: &reason}
	}
	if err := transport.Respond(q.RequestID, response); err != nil {
		return "", false, err
	}
	q.Sent = true
	if response.Decision == "provide" {
		return "Password supplied", true, nil
	}
	return "Denied", true, nil
}

func (q *Question) CancelPassword(transport Responder) bool {
	if q.Sent {
		return false
	}
	q.Sent = true
	reason := "authentication canceled"
	_ = transport.Respond(q.RequestID, binding.ShellGuardSecretResponse{Decision: "decline", Reason: &reason})
	return true
}

// ValidatePassword shares authentication input policy with final batch admission.
// It neither retains the secret nor answers the request.
func (q *Question) ValidatePassword(answers map[int][]string) error {
	question := &q.Questions[0]
	if q.Secret != nil && q.Secret.Kind == binding.GpgPassphrase {
		if question.Done && !question.Skipped && len(answers[0]) == 1 && strings.ContainsAny(answers[0][0], "\r\n\x00") {
			return errors.New("GPG passphrase must be a single line without NUL")
		}
		return nil
	}
	if question.Done && !question.Skipped && len(answers[0]) == 1 && strings.ContainsAny(answers[0][0], "\r\n") {
		return errors.New("sudo password must be a single line")
	}
	return nil
}
