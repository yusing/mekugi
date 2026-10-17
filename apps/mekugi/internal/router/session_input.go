package router

import "github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

func (u *appServerUI) editInput(intent sessionpresentation.DraftEditIntent) bool {
	if err := u.sessionPresentation().EditDraft(intent, u.conversation()); err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	u.mainView.CursorColumn = nil
	u.setNotice("", false)
	u.pruneDraftImages()
	return true
}

func (u *appServerUI) submitInput(intent sessionpresentation.SubmitIntent) error {
	draft, plan, err := u.sessionPresentation().TakeInput(intent, &u.Live, u.conversation(), u.restoring != nil)
	if err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	input := u.prepareDraft(draft)
	if plan.Queued {
		u.conversation().Queued = append(u.conversation().Queued, input)
	} else {
		u.conversation().Unsent = append(u.conversation().Unsent, input)
	}
	u.pruneDraftImages()
	u.setNotice("", false)
	if !plan.Flush {
		return nil
	}
	u.view.Follow()
	return u.flushInput()
}
