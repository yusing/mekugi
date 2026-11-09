package launch

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func openSudoPassword(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, a *sessioncontrol.Approval) {
	id := fmt.Sprintf("%s/%p", a.Guard.Kind, a.Guard)
	header, label := "Sudo password", "sudo password"
	if a.Guard.Kind == "gpg-passphrase" {
		header, label = "GPG passphrase", "GPG passphrase"
	}
	c := &sessioncontrol.Question{Call: &prompt.Call{ID: id, Thread: a.Thread, Turn: a.Turn, Item: id, Local: true,
		Questions: []prompt.Question{{ID: "password", Header: header, IsSecret: true,
			Question: "Enter your " + label + ":\n" + a.Subject + "\nCtrl-] skips and denies authentication."}}}, Sudo: a.Guard}
	addQuestions(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, c, false)
}

func (session *nativeSession) submitSudoPassword(attached *attachedPresentation, picker *composerPicker, clock func() time.Time, editor **composer.Editor, parent context.Context, c *sessioncontrol.Question, answers map[int][]string) error {
	outcome, delivered, err := c.AnswerPassword(answers)
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	if !delivered {
		expireSudoPasswords(attached, &session.Client, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts)
		return nil
	}
	resolveQuestionCall(attached, &session.Client, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, c, "answered")
	recordApproval(attached, &session.approvals, session.review, &session.pendingEditReceipts, session.Client.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
	return session.flushInput(attached, picker, clock, editor, parent)
}

func expireSudoPasswords(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt) bool {
	changed := false
	for _, c := range slices.Clone(questions.calls()) {
		if c.Sudo != nil && !c.Resolved && c.Sudo.Finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if c.Sudo.Outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			resolveQuestionCall(attached, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "interrupted")
			recordApproval(attached, approvals, reviewOwner, receipts, control.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
			changed = true
		}
	}
	return changed
}
