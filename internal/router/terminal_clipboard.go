package router

import "encoding/base64"

// copyText owns clipboard delivery and feedback for /copy, selected text
// (including Ctrl-C), and links. OSC 52 is a request, not confirmed delivery.
func (u *terminalUI) copyText(text string) {
	if text == "" {
		u.main.setNotice("Nothing to copy", true)
		return
	}
	u.clipboard = "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	u.main.setNotice("Copy sent to terminal clipboard", false)
}
