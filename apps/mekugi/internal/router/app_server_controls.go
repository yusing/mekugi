package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func (u *appServerUI) controlsCommand(text string) (bool, error) {
	switch text {
	case "/lock", "/unlock":
		u.interruptLocked = text == "/lock"
		u.deleteDraftRange(0, len(u.Text))
		if u.interruptLocked {
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
	if u.interruptLocked {
		u.lockNotice()
		return nil
	}
	view := u.publishPresentation(u.now())
	return u.interruptSession(sessionpresentation.InterruptIntent{Version: view.Version, Target: view.Interrupt, Mode: sessioncontrol.RestoreWaiting})
}

// Escape expedites pending steers like Codex: interrupt, then submit the
// uncommitted input after the host confirms the turn ended. Ctrl-C restores it.
func (u *appServerUI) keyboardEscape() error {
	if u.interruptLocked {
		u.lockNotice()
		return nil
	}
	view := u.publishPresentation(u.now())
	return u.interruptSession(sessionpresentation.InterruptIntent{Version: view.Version, Target: view.Interrupt, Mode: sessioncontrol.ExpediteSteers})
}
