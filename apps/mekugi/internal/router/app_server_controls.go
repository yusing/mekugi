package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func (u *appServerUI) controlsCommand(text string) (bool, error) {
	switch text {
	case "/lock", "/unlock":
		u.shell.Main.InterruptLocked = text == "/lock"
		u.deleteDraftRange(0, len(u.Text))
		if u.shell.Main.InterruptLocked {
			u.lockNotice()
		} else {
			u.setNotice("Unlocked · Esc and Ctrl-C can interrupt", false)
		}
		return true, nil
	}
	return false, nil
}

func (u *appServerUI) lockNotice() {
	u.setNotice("Locked · /unlock restores Esc and Ctrl-C · /quit exits when idle", false)
}

// Only keyboard cancellation is guarded. Host lifecycle and already-admitted
// interrupts retain their authority, and contextual Escape/copy still work.
func (u *appServerUI) keyboardInterrupt() error {
	if u.shell.Main.InterruptLocked {
		u.lockNotice()
		return nil
	}
	view := u.publishPresentation(u.now())
	return u.interruptSession(sessionpresentation.InterruptIntent{Version: view.Version, Target: view.Interrupt, Mode: sessioncontrol.RestoreWaiting})
}
