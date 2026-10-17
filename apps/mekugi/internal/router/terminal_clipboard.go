package router

import "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

// Clipboard delivery belongs to the terminal; feedback uses the shared notice owner.
func (main *appServerUI) copyText(u *terminalsession.Attachment, text string) {
	notice, warning := u.Frame.CopyText(text)
	setNotice(&main.attached.feedback, main.shell, presentationTime(main.clock), notice, warning)
}
