package router

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func (u *appServerUI) openSudoPassword(a *sessioncontrol.Approval) {
	id := fmt.Sprintf("sudo-password/%p", a.Guard)
	c := &sessioncontrol.Question{Call: &prompt.Call{ID: id, Thread: a.Thread, Turn: a.Turn, Item: id, Local: true,
		Questions: []prompt.Question{{ID: "password", Header: "Sudo password", IsSecret: true,
			Question: "Enter your sudo password:\n" + a.Subject + "\nCtrl-] skips and denies authentication."}}}, Sudo: a.Guard}
	u.addQuestions(c, false)
}

func (u *appServerUI) submitSudoPassword(c *sessioncontrol.Question) error {
	outcome, delivered, err := c.AnswerPassword(u.shell.Prompts.SecretState(c.ID).Answers)
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return nil
	}
	if !delivered {
		u.expireSudoPasswords()
		return nil
	}
	u.resolveQuestionCall(c, "answered")
	recordApproval(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
	return u.flushInput()
}

func (u *appServerUI) expireSudoPasswords() bool {
	changed := false
	for _, c := range slices.Clone(u.questions.calls()) {
		if c.Sudo != nil && !c.Resolved && c.Sudo.Finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if c.Sudo.Outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			u.resolveQuestionCall(c, "interrupted")
			recordApproval(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, outcome)
			changed = true
		}
	}
	return changed
}
