package router

import (
	json "encoding/json/v2"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func (u *appServerUI) titleCommand(text string) bool {
	if fields := strings.Fields(text); len(fields) == 0 || fields[0] != "/title" {
		return false
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, "/title"))
	if name == "" {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Usage: /title <title>", false)
		return true
	}
	if u.Replacement.Pending() {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait for the session to be ready", false)
		return true
	}
	deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	renameSessionTitle(&u.Client, u.titleGenerator, &u.attached.feedback, u.shell, u.clock, &u.dirty, name)
	return true
}

func renameSessionTitle(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, dirty *bool, name string) {
	(*dirty) = true
	control.Names.Manual(control.Thread, name)
	if control.Thread == "" {
		return
	}
	// Suppress generation before persistence, even if transport rejects the save.
	generator.nameChanged(control.Thread, name)
	requestSessionTitle(control, feedback, terminal, clock, dirty, control.Names.Next(control.Thread))
}

// Automatic candidates become visible only after host confirmation.
func persistSessionTitle(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, dirty *bool, update sessionTitleUpdate) {
	if !control.Names.AllowsGenerated(update.thread) || !generator.acceptCandidate(update.thread) {
		return
	}
	requestSessionTitle(control, feedback, terminal, clock, dirty, control.Names.Generated(update.thread, update.name))
}

func requestSessionTitle(control *sessioncontrol.Client[appServerRequest], feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, dirty *bool, save *sessioncontrol.NameSave) {
	if save == nil {
		return
	}
	_, err := control.Requests.Send("thread/name/set", map[string]any{"threadId": save.Thread, "name": save.Name}, "", appServerRequest{title: save})
	if err != nil {
		control.Names.Complete(*save)
		setNotice(feedback, terminal, presentationTime(clock), "Session title could not be saved", false)
		(*dirty) = true
	}
}

func sessionTitleMessage(control *sessioncontrol.Client[appServerRequest], generator *sessionNaming, feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, dirty *bool, title *string, m appserver.Message, update *sessioncontrol.NameSave) bool {
	if update != nil {
		latest := control.Names.Complete(*update)
		if m.Error != nil {
			if update.Thread == control.Thread && latest {
				setNotice(feedback, terminal, presentationTime(clock), "Session title could not be saved: "+m.Error.Message, false)
				(*dirty) = true
			}
		} else {
			(*dirty) = confirmSessionTitle(generator, control.Thread, title, update.Thread, update.Name) || (*dirty)
		}
		requestSessionTitle(control, feedback, terminal, clock, dirty, control.Names.Next(update.Thread))
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
		(*dirty) = confirmSessionTitle(generator, control.Thread, title, params.ThreadID, params.ThreadName) || (*dirty)
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
