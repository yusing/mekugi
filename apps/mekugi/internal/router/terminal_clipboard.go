package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// Clipboard delivery belongs to the terminal; feedback uses the shared notice owner.
func copyText(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, u *terminalsession.Attachment, text string) {
	notice, warning := u.Frame.CopyText(text)
	setNotice(feedback, terminal, presentationTime(clock), notice, warning)
}
