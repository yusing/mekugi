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
		u.setNotice("Usage: /title <title>", false)
		return true
	}
	if u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return true
	}
	u.deleteDraftRange(0, len(u.Text))
	u.setNotice("", false)
	u.renameSessionTitle(name)
	return true
}

func (u *appServerUI) renameSessionTitle(name string) {
	u.dirty = true
	u.names.Manual(u.Thread, name)
	if u.Thread == "" {
		return
	}
	// Suppress generation before persistence, even if transport rejects the save.
	u.titleGenerator.nameChanged(u.Thread, name)
	u.requestSessionTitle(u.names.Next(u.Thread))
}

// Automatic candidates become visible only after host confirmation.
func (u *appServerUI) persistSessionTitle(update sessionTitleUpdate) {
	if !u.names.AllowsGenerated(update.thread) || !u.titleGenerator.acceptCandidate(update.thread) {
		return
	}
	u.requestSessionTitle(u.names.Generated(update.thread, update.name))
}

func (u *appServerUI) requestSessionTitle(save *sessioncontrol.NameSave) {
	if save == nil {
		return
	}
	_, err := u.requests.Send("thread/name/set", map[string]any{"threadId": save.Thread, "name": save.Name}, "", appServerRequest{title: save})
	if err != nil {
		u.names.Complete(*save)
		u.setNotice("Session title could not be saved", false)
		u.dirty = true
	}
}

func (u *appServerUI) sessionTitleMessage(m appserver.Message, update *sessioncontrol.NameSave) bool {
	if update != nil {
		latest := u.names.Complete(*update)
		if m.Error != nil {
			if update.Thread == u.Thread && latest {
				u.setNotice("Session title could not be saved: "+m.Error.Message, false)
				u.dirty = true
			}
		} else {
			u.confirmSessionTitle(update.Thread, update.Name)
		}
		u.requestSessionTitle(u.names.Next(update.Thread))
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
		u.confirmSessionTitle(params.ThreadID, params.ThreadName)
	}
	return true
}

func (u *appServerUI) confirmSessionTitle(thread, name string) {
	if thread == "" {
		return
	}
	if u.titleGenerator != nil {
		u.titleGenerator.nameChanged(thread, name)
	}
	if thread == u.Thread {
		u.title, u.dirty = name, true
	}
}
