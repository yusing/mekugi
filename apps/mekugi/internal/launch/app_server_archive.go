package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

const archiveConfirmChoice uint64 = 2

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:240:278 and
// codex-rs/tui/src/app/event_dispatch.rs:3705:3771@ddb33e34b.
// Codex owns stopping the turn and archiving the thread and its descendants.
func (session *nativeSession) showArchivePicker(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool) {
	if session.Client.Thread == "" || session.restoring != nil || session.Client.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return
	}
	session.deleteDraftRange(attached, terminal, picker, editor, side, clock, overlay, 0, len((*editor).Text))
	picker.cancelScan()
	p := picker
	picker.openMenu("archive")
	p.Target, p.Problem, p.Loading = completion.Target{}, "", false
	p.Choices = []completion.Choice{
		{ID: 1, Name: "No, don't archive", Description: "Return to the current session"},
		{ID: archiveConfirmChoice, Name: "Yes, archive", Description: "Archive this session now"},
	}
}

func archivePrompt(control *sessioncontrol.Live) string {
	if control.Turn != "" || control.Starting() || control.InputPending() {
		return "This will stop the current turn and archive the session."
	}
	return "Are you sure? This will archive the current session."
}

func chooseArchive(picker *composerPicker, control *sessioncontrol.Client[appServerRequest], feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, confirm bool) {
	p := picker
	p.Kind, p.Open, p.Choices = "", false, nil

	if confirm {
		control.Replacement.Archive()
		if err := control.Request("thread/archive", map[string]any{"threadId": control.Thread}); err != nil {
			control.Replacement.Finish()
			setNotice(feedback, terminal, presentationTime(clock), "Failed to archive current thread: "+err.Error(), true)
		} else {
			setNotice(feedback, terminal, presentationTime(clock), "Archiving session…", false)
		}
	}
}
