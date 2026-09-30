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
	u.setNotice("Locked · /unlock to interrupt or exit with Ctrl-C", false)
}

// Only keyboard cancellation is guarded. Host lifecycle and already-admitted
// interrupts retain their authority, and contextual Escape/copy still work.
func (u *appServerUI) keyboardInterrupt() error {
	if u.interruptLocked {
		u.lockNotice()
		return nil
	}
	return u.interruptTurn()
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
		if u.activeChildren == nil {
			u.activeChildren = make(map[string]bool)
		}
		u.activeChildren[thread] = true
		u.ensureShell()
		if u.shell.journalOpen && !u.shell.diffOpen {
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
