package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

const archiveConfirmChoice uint64 = 2

// Source: codex-rs/tui/src/chatwidget/slash_dispatch.rs:240:278 and
// codex-rs/tui/src/app/event_dispatch.rs:3705:3771@ddb33e34b.
// Codex owns stopping the turn and archiving the thread and its descendants.
func showArchivePicker(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, applied preferences.Applied, tiers **inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool) {
	if control.Thread == "" || restoring || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return
	}
	deleteDraftRange(attached, terminal, control, picker, editor, applied, (*tiers), title, backend, restoring, side, clock, questions, approvals, overlay, owned, 0, len((*editor).Text))
	picker.cancelScan()
	p := picker
	picker.openMenu("archive")
	p.archiveTarget = ""
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
	target := p.archiveTarget
	p.archiveTarget = ""
	if confirm && target != "" {
		if _, err := control.Requests.Send("thread/archive", map[string]any{"threadId": target}, "rewind/archive", appServerRequest{label: "rewind/archive", metadata: target}); err != nil {
			setNotice(feedback, terminal, presentationTime(clock), "Original thread "+target+" still exists: "+err.Error(), true)
		} else {
			setNotice(feedback, terminal, presentationTime(clock), "Archiving original thread "+target+"…", false)
		}
		return
	}
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
