package router

func (u *appServerUI) controlsCommand(text string) (bool, error) {
	switch text {
	case "/lock", "/unlock":
		u.interruptLocked = text == "/lock"
		u.deleteDraftRange(0, len(u.draft))
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
	if u.runtime != nil {
		if !u.runtime.busy && u.runtime.shell == nil {
			return nil
		}
		if err := u.stopRuntimeJournalTurn(); err != nil {
			u.setNotice("Journal stop receipt unavailable: "+err.Error(), true)
		}
		u.status = "Interrupting…"
		return u.runtime.client.Interrupt(u.ctx)
	}
	u.sendSteersAfterInterrupt = false
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
	if u.sendSteersAfterInterrupt || u.steerInterruptAckPending {
		return nil
	}
	if u.turn != "" && u.interruption.target == "" && !u.compaction.pending() {
		u.sendSteersAfterInterrupt = len(u.steers) > 0 ||
			u.submission.turn != "" && !u.submission.committed ||
			len(u.unsent) > 0 && u.unsent[0].text != "/compact"
	}
	err := u.interruptTurn()
	if err != nil {
		u.sendSteersAfterInterrupt = false
	}
	return err
}

// Live thread IDs are stable across nickname metadata updates. Pending spawns
// count too, so one completion cannot hide another child's imminent turn.
func (u *appServerUI) updateAgentPane(method string, event appServerEvent) {
	if u.restoring != nil {
		return
	}
	thread := event.ThreadID
	if event.Item.Type == "subAgentActivity" && method == "item/completed" {
		thread = event.Item.AgentThreadID
		switch event.Item.Kind {
		case "started":
			method = "thread/started"
		case "completed", "interrupted":
			method = "turn/completed"
		}
	} else if method == "thread/started" {
		thread = event.Thread.ID
	}
	if thread == "" || thread == u.thread {
		return
	}
	switch method {
	case "thread/started", "turn/started":
		firstChild := len(u.activeChildren) == 0
		if u.activeChildren == nil {
			u.activeChildren = make(map[string]bool)
		}
		u.activeChildren[thread] = true
		u.ensureShell()
		if firstChild && u.shell.journalOpen && !u.shell.diffOpen {
			u.shell.journalOpen, u.shell.autoActivity = false, true
			if u.shell.focus == 4 {
				u.shell.focus = 0
			}
		}
	case "turn/completed":
		delete(u.activeChildren, thread)
		if len(u.activeChildren) == 0 && u.shell != nil && u.shell.autoActivity {
			u.shell.journalOpen, u.shell.autoActivity = true, false
		}
	}
}
