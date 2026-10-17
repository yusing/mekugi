package router

import (
	json "encoding/json/v2"
	"strings"

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
	u.deleteDraftRange(0, len(u.Text))
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	u.renameSessionTitle(name)
	return true
}

func (u *appServerUI) renameSessionTitle(name string) {
	u.dirty = true
	u.Names.Manual(u.Thread, name)
	if u.Thread == "" {
		return
	}
	// Suppress generation before persistence, even if transport rejects the save.
	u.titleGenerator.nameChanged(u.Thread, name)
	u.requestSessionTitle(u.Names.Next(u.Thread))
}

// Automatic candidates become visible only after host confirmation.
func (u *appServerUI) persistSessionTitle(update sessionTitleUpdate) {
	if !u.Names.AllowsGenerated(update.thread) || !u.titleGenerator.acceptCandidate(update.thread) {
		return
	}
	u.requestSessionTitle(u.Names.Generated(update.thread, update.name))
}

func (u *appServerUI) requestSessionTitle(save *sessioncontrol.NameSave) {
	if save == nil {
		return
	}
	_, err := u.Requests.Send("thread/name/set", map[string]any{"threadId": save.Thread, "name": save.Name}, "", appServerRequest{title: save})
	if err != nil {
		u.Names.Complete(*save)
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Session title could not be saved", false)
		u.dirty = true
	}
}

func (u *appServerUI) sessionTitleMessage(m appserver.Message, update *sessioncontrol.NameSave) bool {
	if update != nil {
		latest := u.Names.Complete(*update)
		if m.Error != nil {
			if update.Thread == u.Thread && latest {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Session title could not be saved: "+m.Error.Message, false)
				u.dirty = true
			}
		} else {
			u.dirty = confirmSessionTitle(u.titleGenerator, u.Thread, &u.title, update.Thread, update.Name) || u.dirty
		}
		u.requestSessionTitle(u.Names.Next(update.Thread))
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
		u.dirty = confirmSessionTitle(u.titleGenerator, u.Thread, &u.title, params.ThreadID, params.ThreadName) || u.dirty
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
