package router

import (
	json "encoding/json/v2"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
)

type sessionTitleRequest struct {
	sessionTitleUpdate
	manual bool
}

func (u *appServerUI) titleCommand(text string) bool {
	if fields := strings.Fields(text); len(fields) == 0 || fields[0] != "/title" {
		return false
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, "/title"))
	if name == "" {
		u.setNotice("Usage: /title <title>", false)
		return true
	}
	if u.replacement.pending() {
		u.setNotice("Wait for the session to be ready", false)
		return true
	}
	u.deleteDraftRange(0, len(u.draft))
	u.setNotice("", false)
	u.renameSessionTitle(name)
	return true
}

func (u *appServerUI) renameSessionTitle(name string) {
	u.dirty = true
	if u.thread == "" {
		u.pendingTitle = name
		return
	}
	if u.titleRenames == nil {
		u.titleRenames = make(map[string]string)
	}
	u.titleRenames[u.thread] = name
	// Stop naming before persistence, including a result already in flight.
	u.titleGenerator.named(u.thread, &name)
	u.flushTitleRename(u.thread)
}

func (u *appServerUI) flushTitleRename(thread string) {
	name := u.titleRenames[thread]
	if name == "" {
		return
	}
	for _, request := range u.titleRequests {
		if request.thread == thread {
			return // Serialize writes so an older name cannot win at the host.
		}
	}
	if !u.requestSessionTitle(sessionTitleUpdate{thread: thread, name: name}, true) {
		delete(u.titleRenames, thread)
	}
}

// Automatic naming results are displayed only after host confirmation.
func (u *appServerUI) persistSessionTitle(update sessionTitleUpdate) {
	if u.titleRenames[update.thread] != "" || u.titleGenerator.named(update.thread, nil) {
		return // A host rename won the race against automatic naming.
	}
	u.requestSessionTitle(update, false)
}

func (u *appServerUI) requestSessionTitle(update sessionTitleUpdate, manual bool) bool {
	id, err := u.requestAs("thread/name/set", "session/title", map[string]any{"threadId": update.thread, "name": update.name})
	if err != nil {
		u.setNotice("Session title could not be saved", false)
		u.dirty = true
		return false
	}
	if u.titleRequests == nil {
		u.titleRequests = make(map[string]sessionTitleRequest)
	}
	u.titleRequests[id] = sessionTitleRequest{sessionTitleUpdate: update, manual: manual}
	return true
}

func (u *appServerUI) sessionTitleMessage(m appserver.Message) bool {
	if update, ok := u.titleRequests[string(m.ID)]; ok {
		delete(u.titleRequests, string(m.ID))
		delete(u.requests, string(m.ID))
		if update.manual && u.titleRenames[update.thread] == update.name {
			delete(u.titleRenames, update.thread)
		}
		if m.Error != nil {
			if update.thread == u.thread && u.titleRenames[update.thread] == "" {
				u.setNotice("Session title could not be saved: "+m.Error.Message, false)
				u.dirty = true
			}
		} else {
			u.confirmSessionTitle(update.thread, update.name)
		}
		u.flushTitleRename(update.thread)
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
		u.titleGenerator.named(thread, &name)
		u.titleGenerator.cache.set(thread, name)
	} else if u.proxy != nil {
		u.proxy.titles.set(thread, name)
	}
	if thread == u.thread {
		u.title, u.dirty = name, true
	}
}

func (u *appServerUI) mainHeaderRight(width int, focused bool) string {
	scroll := scrollLabel(u.view)
	name := u.title
	if u.thread == "" && u.pendingTitle != "" {
		name = u.pendingTitle
	} else if pending := u.titleRenames[u.thread]; pending != "" {
		name = pending
	}
	title := strings.Join(strings.Fields(pickerText(name)), " ")
	if title == "" {
		return scroll
	}
	// Account for both labels' framing spaces and one separating border cell.
	available := width - 2 - ansi.StringWidth(nativeTitle(1, "Main", u.questionBadge(), focused)) - 5
	if scroll != "" && available > ansi.StringWidth(scroll)+6 {
		title = ansi.Truncate(title, available-ansi.StringWidth(scroll)-3, "…")
		return scroll + " · " + title
	}
	return ansi.Truncate(title, max(0, available), "…")
}
