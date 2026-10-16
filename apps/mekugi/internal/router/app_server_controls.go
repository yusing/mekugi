package router

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
	u.SendSteersAfterInterrupt = false
	return u.interruptTurn()
}

// Escape expedites pending steers like Codex: interrupt, then submit the
// uncommitted input after the host confirms the turn ended. Ctrl-C restores it.
func (u *appServerUI) keyboardEscape() error {
	if u.interruptLocked {
		u.lockNotice()
		return nil
	}
	// Completion can arrive before either acknowledgement. A repeated Escape
	// must not restore the waiting input or interrupt its replacement turn.
	if !u.PrepareEscape(len(u.conversation().Unsent) > 0 && u.conversation().Unsent[0].Text != "/compact") {
		return nil
	}
	err := u.interruptTurn()
	if err != nil {
		u.SendSteersAfterInterrupt = false
	}
	return err
}
