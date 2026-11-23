package launch

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func openShellGuardSecret(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, m appserver.Message, params binding.ShellGuardSecretParams) {
	id := string(params.Kind) + "/" + string(m.ID)
	header, label := "Sudo password", "sudo password"
	if params.Kind == binding.GpgPassphrase {
		header, label = "GPG passphrase", "GPG passphrase"
	}
	thread, turn, item := params.ThreadID, params.TurnID, params.ItemID
	if item != "" {
		if owner := approvalItem(attached, thread, "", item); owner != nil {
			thread, turn, item = owner.Thread, owner.Turn, owner.Item
		}
	}
	subject := shellCommandString(params.Argv[0], params.Argv[1:])
	p := params
	p.ItemID = item
	c := &sessioncontrol.Question{
		Call: &prompt.Call{
			ID:     id,
			Thread: thread,
			Turn:   turn,
			Item:   id,
			Local:  true,
			Questions: []prompt.Question{{
				ID:       "password",
				Header:   header,
				IsSecret: true,
				Question: "Enter your " + label + ":\n" + subject + "\nCtrl-] skips and denies authentication.",
			}},
		},
		RequestID: slices.Clone(m.ID),
		Secret:    &p,
	}
	notifications.blockNotification(&attached.feedback, clock, m)
	addQuestions(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, c, false)
}

func (session *nativeSession) submitShellGuardSecret(attached *attachedPresentation, picker *composerPicker, clock func() time.Time, editor **composer.Editor, parent context.Context, c *sessioncontrol.Question, secretEditors map[int]composer.State, answers map[int][]string) error {
	outcome, delivered, err := c.AnswerPassword(session.Client.Requests, answers)
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	clear(secretEditors)
	clear(answers)
	if !delivered {
		return nil
	}
	resolveQuestionCall(attached, &session.Client, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, c, "answered")
	if c.Secret != nil {
		recordApproval(attached, &session.approvals, session.review, &session.pendingEditReceipts, session.Client.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Secret.ItemID}}, outcome)
	}
	return session.flushInput(attached, picker, clock, editor, parent)
}
