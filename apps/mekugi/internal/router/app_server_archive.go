package router

import "github.com/yusing/mekugi/modules/conversation/completion"

const archiveConfirmChoice uint64 = 2

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:240:278 and
// codex-rs/tui/src/app/event_dispatch.rs:3705:3771@ddb33e34b.
// Codex owns stopping the turn and archiving the thread and its descendants.
func (u *appServerUI) showArchivePicker() {
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return
	}
	u.deleteDraftRange(0, len(u.Text))
	u.cancelPickerScan()
	p := &u.picker
	u.openMenu("archive")
	p.Target, p.Problem, p.Loading = completion.Target{}, "", false
	p.Choices = []completion.Choice{
		{ID: 1, Name: "No, don't archive", Description: "Return to the current session"},
		{ID: archiveConfirmChoice, Name: "Yes, archive", Description: "Archive this session now"},
	}
}

func (u *appServerUI) archivePrompt() string {
	if u.Turn != "" || u.Starting() || u.InputPending() {
		return "This will stop the current turn and archive the session."
	}
	return "Are you sure? This will archive the current session."
}

func (u *appServerUI) chooseArchive(confirm bool) {
	p := &u.picker
	p.Kind, p.Open, p.Choices = "", false, nil
	if confirm {
		u.Replacement.Archive()
		if err := u.Request("thread/archive", map[string]any{"threadId": u.Thread}); err != nil {
			u.Replacement.Finish()
			u.setNotice("Failed to archive current thread: "+err.Error(), true)
		} else {
			u.setNotice("Archiving session…", false)
		}
	}
}
