package router

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/modules/conversation/prompt"

	"github.com/yusing/mekugi/modules/sudoapproval/sudo"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

func (u *appServerUI) openSudoPassword(a *nativeApproval) {
	id := fmt.Sprintf("sudo-password/%p", a.guard)
	c := &nativeQuestionCall{Call: &prompt.Call{ID: id, Thread: a.Thread, Turn: a.Turn, Item: id, Local: true,
		Questions: []prompt.Question{{ID: "password", Header: "Sudo password", IsSecret: true,
			Question: "Enter your sudo password:\n" + a.Subject + "\nCtrl-] skips and denies authentication."}}}, sudo: a.guard}
	u.addQuestions(c, false)
}

func (u *appServerUI) submitSudoPassword(c *nativeQuestionCall) error {
	if c.sudo.finished() {
		u.expireSudoPasswords()
		return nil
	}
	q := &c.Questions[0]
	reply := shellapproval.Reply{Reason: "sudo authentication denied"}
	if q.Done && !q.Skipped && len(c.secretAnswers[0]) == 1 {
		password := c.secretAnswers[0][0]
		if !sudo.ValidPassword(password) {
			u.setNotice("sudo password must be a single line", true)
			return nil
		}
		reply.OK, reply.Password = true, password
	}
	c.Sent = true
	c.sudo.reply <- reply
	u.resolveQuestionCall(c, "answered")
	outcome := "Denied"
	if reply.OK {
		outcome = "Password supplied"
	}
	u.recordApproval(&nativeApproval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.sudo.item}}, outcome)
	return u.flushInput()
}

func (u *appServerUI) expireSudoPasswords() bool {
	changed := false
	for _, c := range slices.Clone(u.questionCalls()) {
		if c.sudo != nil && !c.Resolved && c.sudo.finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if c.sudo.outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			u.resolveQuestionCall(c, "interrupted")
			u.recordApproval(&nativeApproval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.sudo.item}}, outcome)
			changed = true
		}
	}
	return changed
}
