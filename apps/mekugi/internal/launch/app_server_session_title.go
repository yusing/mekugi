package launch

import (
	json "encoding/json/v2"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func titleCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, generator *sessionNaming, text string) bool {
	if fields := strings.Fields(text); len(fields) == 0 || fields[0] != "/title" {
		return false
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, "/title"))
	if name == "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Usage: /title <title>", false)
		return true
	}
	if control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return true
	}
	deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, (*owned), 0, len((*editor).Text))
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	renameSessionTitle(control, generator, &attached.feedback, terminal, clock, name)
	return true
}

func renameSessionTitle(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, name string) {
	control.Names.Manual(control.Thread, name)
	if control.Thread == "" {
		return
	}
	// Suppress generation before persistence, even if transport rejects the save.
	generator.nameChanged(control.Thread, name)
	requestSessionTitle(control, feedback, terminal, clock, control.Names.Next(control.Thread))
}

// Automatic candidates become visible only after host confirmation.
func persistSessionTitle(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, update sessionTitleUpdate) {
	if !control.Names.AllowsGenerated(update.thread) || !generator.acceptCandidate(update.thread) {
		return
	}
	requestSessionTitle(control, feedback, terminal, clock, control.Names.Generated(update.thread, update.name))
}

func requestSessionTitle(control *sessioncontrol.Client[appServerRequest], feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, save *sessioncontrol.NameSave) {
	if save == nil {
		return
	}
	_, err := control.Requests.Send("thread/name/set", map[string]any{"threadId": save.Thread, "name": save.Name}, "", appServerRequest{title: save})
	if err != nil {
		control.Names.Complete(*save)
		setNotice(feedback, terminal, presentationTime(clock), "Session title could not be saved", false)
	}
}

func sessionTitleMessage(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, title *string, m appserver.Message, update *sessioncontrol.NameSave) bool {
	if update != nil {
		latest := control.Names.Complete(*update)
		if m.Error != nil {
			if update.Thread == control.Thread && latest {
				setNotice(feedback, terminal, presentationTime(clock), "Session title could not be saved: "+m.Error.Message, false)
			}
		} else {
			confirmSessionTitle(generator, control.Thread, title, update.Thread, update.Name)
		}
		requestSessionTitle(control, feedback, terminal, clock, control.Names.Next(update.Thread))
		return true
	}
	if m.Method != "thread/name/updated" {
		return false
	}
	var params struct {
		ThreadID   string `json:"threadId"`
		ThreadName string `json:"threadName"`
	}
	if json.Unmarshal(m.Params, &params) == nil {
		confirmSessionTitle(generator, control.Thread, title, params.ThreadID, params.ThreadName)
	}
	return true
}

func confirmSessionTitle(generator *sessionNaming, main string, title *string, thread, name string) bool {
	if thread == "" {
		return false
	}
	if generator != nil {
		generator.nameChanged(thread, name)
	}
	if thread == main {
		*title = name
		return true
	}
	return false
}
