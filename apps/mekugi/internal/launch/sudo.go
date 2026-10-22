package launch

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func openSudoPassword(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, a *sessioncontrol.Approval) {
	id := fmt.Sprintf("sudo-password/%p", a.Guard)
	c := &sessioncontrol.Question{Call: &prompt.Call{ID: id, Thread: a.Thread, Turn: a.Turn, Item: id, Local: true,
		Questions: []prompt.Question{{ID: "password", Header: "Sudo password", IsSecret: true,
			Question: "Enter your sudo password:\n" + a.Subject + "\nCtrl-] skips and denies authentication."}}}, Sudo: a.Guard}
	addQuestions(attached, terminal, control, picker, editor, questions, approvals, clock, overlay, notifications, c, false)
}

func submitSudoPassword(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model string, tiers *inferencegateway.Tiers, environment []string, owned map[string]bool, hook *approvalHookCheck, editor **composer.Editor, parent context.Context, restoring bool, observations *sessioncontrol.Observations, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, c *sessioncontrol.Question, answers map[int][]string) error {
	outcome, delivered, err := c.AnswerPassword(answers)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	if !delivered {
		expireSudoPasswords(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts)
		return nil
	}
	resolveQuestionCall(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "answered")
	recordApproval(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, model, tiers, environment, owned, hook, editor, parent, restoring, observations, reviewOwner, receipts)
}

func expireSudoPasswords(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt) bool {
	changed := false
	for _, c := range slices.Clone(questions.calls()) {
		if c.Sudo != nil && !c.Resolved && c.Sudo.Finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if c.Sudo.Outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			resolveQuestionCall(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "interrupted")
			recordApproval(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
			changed = true
		}
	}
	return changed
}
