package router

import (
	json "encoding/json/v2"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
)

// Naming results go through the host's metadata API, never straight to the pane.
func (u *appServerUI) persistSessionTitle(update sessionTitleUpdate) {
	if u.titleGenerator.named(update.thread, nil) {
		return // A host rename won the race against automatic naming.
	}
	if update.err != nil {
		if update.thread == u.thread {
			u.setNotice("Session title could not be generated", false)
			u.dirty = true
		}
		return
	}
	id, err := u.requestAs("thread/name/set", "session/title", map[string]any{"threadId": update.thread, "name": update.name})
	if err != nil {
		u.setNotice("Session title could not be saved", false)
		u.dirty = true
		return
	}
	if u.titleRequests == nil {
		u.titleRequests = make(map[string]sessionTitleUpdate)
	}
	u.titleRequests[id] = update
}

func (u *appServerUI) sessionTitleMessage(m appserver.Message) bool {
	if update, ok := u.titleRequests[string(m.ID)]; ok {
		delete(u.titleRequests, string(m.ID))
		delete(u.requests, string(m.ID))
		if m.Error != nil {
			if update.thread == u.thread {
				u.setNotice("Session title could not be saved: "+m.Error.Message, false)
				u.dirty = true
			}
			return true
		}
		u.confirmSessionTitle(update.thread, update.name)
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
	title := strings.Join(strings.Fields(pickerText(u.title)), " ")
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
