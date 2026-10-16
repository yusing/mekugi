package router

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:240:278 and
// codex-rs/tui/src/app/event_dispatch.rs:3705:3771@ddb33e34b.
// Codex owns stopping the turn and archiving the thread and its descendants.
func (u *appServerUI) showArchivePicker() {
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return
	}
	u.deleteDraftRange(0, len(u.draft))
	u.cancelPickerScan()
	p := &u.picker
	p.modal, p.open, p.selected, p.top = "archive", true, 0, 0
	p.target, p.problem, p.loading = composerTarget{}, "", false
	p.choices = []composerChoice{
		{name: "No, don't archive", description: "Return to the current session"},
		{name: "Yes, archive", description: "Archive this session now"},
	}
}

func (u *appServerUI) archivePrompt() string {
	if u.Turn != "" || u.Starting() || u.InputPending() {
		return "This will stop the current turn and archive the session."
	}
	return "Are you sure? This will archive the current session."
}

func (u *appServerUI) archivePickerKey(key string) bool {
	p := &u.picker
	switch key {
	case "\x1b[200~", "\x1b[201~":
		return false
	case "\x1b", "\x03":
		p.modal, p.open, p.choices = "", false, nil
	case "\x1b[A", "\x1bOA", "\x10", "\x1b[B", "\x1bOB", "\x0e", "\t":
		p.selected = 1 - p.selected
	case "\r":
		confirm := p.selected == 1
		p.modal, p.open, p.choices = "", false, nil
		if confirm {
			u.Replacement.Archive()
			if err := u.request("thread/archive", map[string]any{"threadId": u.Thread}); err != nil {
				u.Replacement.Finish()
				u.setNotice("Failed to archive current thread: "+err.Error(), true)
			} else {
				u.setNotice("Archiving session…", false)
			}
		}
	}
	return true
}
