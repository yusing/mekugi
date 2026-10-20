package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/vcsguard"
)

func (u *appServerUI) openSudoPassword(a *nativeApproval) {
	c := &nativeQuestionCall{
		thread: a.thread, turn: a.turn, item: fmt.Sprintf("sudo-password/%p", a.guard), sudo: a.guard,
		questions: []nativeQuestion{{ID: "password", Header: "Sudo password", IsSecret: true,
			Question: "Enter your sudo password:\n" + a.subject + "\nCtrl-] skips and denies authentication."}},
	}
	u.questions.calls = append(u.questions.calls, c)
	u.questions.autoOpen = true
	u.autoOpenQuestions()
}

func (u *appServerUI) submitSudoPassword(c *nativeQuestionCall) error {
	if c.sudo.finished() {
		u.expireSudoPasswords()
		return nil
	}
	q := &c.questions[0]
	reply := vcsguard.Reply{Reason: "sudo authentication denied"}
	if q.done && !q.skipped && len(q.answer) == 1 {
		password := q.answer[0]
		if strings.ContainsAny(password, "\r\n") {
			u.setNotice("sudo password must be a single line", true)
			return nil
		}
		reply.OK, reply.Password = true, password
	}
	c.sent = true
	c.sudo.reply <- reply
	u.resolveQuestionCall(c, "answered")
	outcome := "Denied"
	if reply.OK {
		outcome = "Password supplied"
	}
	u.recordApproval(&nativeApproval{thread: c.thread, turn: c.turn, item: c.sudo.item}, outcome)
	return u.flushInput()
}

func (u *appServerUI) expireSudoPasswords() bool {
	changed := false
	for _, c := range slices.Clone(u.questions.calls) {
		if c.sudo != nil && !c.resolved && c.sudo.finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if c.sudo.outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			u.resolveQuestionCall(c, "interrupted")
			u.recordApproval(&nativeApproval{thread: c.thread, turn: c.turn, item: c.sudo.item}, outcome)
			changed = true
		}
	}
	return changed
}
