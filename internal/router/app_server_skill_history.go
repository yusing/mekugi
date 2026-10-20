package router

import (
	json "encoding/json/v2"

	"github.com/yusing/mekugi/internal/appserver"
)

// Skill counts need the whole current context, independent of the deliberately
// partial Activity transcript. This is a bounded observation, never a child resume.
type appServerSkillHistory struct {
	request string
	cursors map[string]bool
	names   activeSkillSet
	bytes   int
}

func (u *appServerUI) startSkillHistory() {
	if u.restoring != nil || u.replacement.target != "" {
		return
	}
	// One scan at a time keeps background history I/O independently bounded.
	for _, h := range u.childHistory {
		if h.skills != nil && h.skills.request != "" {
			return
		}
	}
	for _, h := range u.childHistory {
		if h.root != u.thread || h.skills != nil {
			continue
		}
		h.skills = &appServerSkillHistory{cursors: make(map[string]bool)}
		u.agents.activeSkills()
		if u.agents.skillHistory[u.session.path(h.info.ID)] {
			continue // A live boundary already established the current context.
		}
		if err := u.requestSkillHistory(h, ""); err != nil {
			u.failSkillHistory(h, err.Error())
		}
		return
	}
}

func (u *appServerUI) requestSkillHistory(h *appServerChildHistory, cursor string) error {
	// Omit turnId to scan across turns, including inherited fork items. Only
	// opaque cursors may continue a thread-wide read; item anchors are turn-scoped.
	params := map[string]any{"threadId": h.info.ID, "sortDirection": "desc", "limit": appServerHistoryPageSize}
	if cursor != "" {
		params["cursor"] = cursor
	}
	id, err := u.requestAs("thread/items/list", "activity-skills/"+h.info.ID, params)
	h.skills.request = id
	return err
}

func (u *appServerUI) failSkillHistory(h *appServerChildHistory, message string) error {
	h.skills.request, h.skills.names = "", nil
	u.restoreHistoryNotice("Could not restore active skills for "+u.session.path(h.info.ID)+": "+message, false)
	return nil
}

func (u *appServerUI) skillHistoryResponse(thread string, m appserver.Message) error {
	h := u.childHistory[thread]
	if h == nil || h.root != u.thread || h.skills == nil || h.skills.request != string(m.ID) {
		return nil
	}
	scan := h.skills
	scan.request = ""
	if m.Error != nil {
		return u.failSkillHistory(h, m.Error.Message)
	}
	scan.bytes += len(m.Result)
	if scan.bytes > restoredRolloutLimit || len(scan.cursors) >= 1024 {
		return u.failSkillHistory(h, "Current-context history exceeds the observation limit; the count is unavailable.")
	}
	var page struct {
		Data []struct {
			TurnID string        `json:"turnId"`
			Item   appServerItem `json:"item"`
		} `json:"data"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(m.Result, &page); err != nil {
		return u.failSkillHistory(h, "Invalid item page: "+err.Error())
	}
	for _, entry := range page.Data {
		if entry.TurnID == "" || entry.Item.ID == "" {
			return u.failSkillHistory(h, "Item page returned no item or turn identity.")
		}
		item := entry.Item
		if item.Type == "contextCompaction" && item.Status != "inProgress" {
			page.NextCursor = ""
			break
		}
		native := &liveActivityNativeItem{thread: thread, turn: entry.TurnID, item: item.ID, status: item.Status, phase: "item/completed", command: item.Command}
		record := liveActivityRecord{activityPaneEntry: activityPaneEntry{Kind: "tool", native: native}}
		switch item.Type {
		case "userMessage":
			record.Kind, native.item = "attachments", item.ID+"/attachments"
			native.attachments = appServerAttachmentBlocks(u.session.cwd, item.Content)
		case "commandExecution":
			u.restoreCommandSegments(&record.activityPaneEntry, item, u.session.cwd)
			if item.ExitCode != nil && *item.ExitCode != 0 && len(native.segments) == 0 {
				continue
			}
			record.Text = appServerToolText(item, u.session.cwd)
		default:
			continue
		}
		record.blocks = parseLiveActivity(record.activityPaneEntry)
		scan.names = scan.names.with(skillLoads(record)...)
	}
	if page.NextCursor != "" {
		if scan.cursors[page.NextCursor] {
			return u.failSkillHistory(h, "Item history returned a repeated pagination cursor.")
		}
		scan.cursors[page.NextCursor] = true
		if err := u.requestSkillHistory(h, page.NextCursor); err != nil {
			u.failSkillHistory(h, err.Error())
		}
		return nil
	}
	v, name := u.agents, u.session.path(h.info.ID)
	// Observe a live reset before adopting the historical snapshot. Its newer
	// context wins, even if the reset's entry has already left the feed.
	v.activeSkills()
	if !v.skillHistory[name] {
		if v.retiredSkills == nil {
			v.retiredSkills = make(map[string]activeSkillSet)
		}
		v.retiredSkills[name] = h.skills.names.with(v.retiredSkills[name]...)
		v.skillHistory[name] = true
	}
	h.skills.names = nil
	v.skills = nil
	u.dirty = true
	return nil
}
