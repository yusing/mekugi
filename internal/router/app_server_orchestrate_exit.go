package router

// The ordinary composer notice owns confirmation, not a second modal input flow.
func (u *appServerUI) quitConfirmationKey(key string) bool {
	if !u.quitConfirmation {
		return false
	}
	u.setNotice("", false)
	switch key {
	case "\r":
		u.orchestrationOwner().orchestrateClosing = true
		u.draft, u.quitRequested = "", true
		return true
	case "\x1b", "\x03":
		return true
	}
	return false
}
