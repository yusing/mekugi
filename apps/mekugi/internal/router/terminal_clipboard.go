package router

// Clipboard delivery belongs to the terminal; feedback uses the shared notice owner.
func (u *terminalUI) copyText(text string) {
	notice, warning := u.Frame.CopyText(text)
	u.main.setNotice(notice, warning)
}
