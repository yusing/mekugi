package launch

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/scope"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Skill counts need the whole current context, independent of the deliberately
// partial Activity transcript. This is a bounded observation, never a child resume.
type appServerSkillHistory struct {
	request string
	cursors map[string]bool
	names   activeSkillSet
	bytes   int
}

func startSkillHistory(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore) {
	if (*restoring) != nil || control.Replacement.Target != "" {
		return
	}
	// One scan at a time keeps background history I/O independently bounded.
	for thread := range attached.session.Threads() {
		h := childHistory(&attached.session, childKey, thread)
		if h == nil {
			continue
		}
		if h.skills != nil && h.skills.request != "" {
			return
		}
	}
	for thread := range attached.session.Threads() {
		h := childHistory(&attached.session, childKey, thread)
		if h == nil {
			continue
		}
		if h.skills != nil {
			continue
		}
		h.skills = &appServerSkillHistory{cursors: make(map[string]bool)}
		if !attached.childSource.NeedsSkillHistory(attached.session.Caller(h.info.ID)) {
			continue // A live boundary already established the current context.
		}
		if err := requestSkillHistory(control.Requests, h, ""); err != nil {
			failSkillHistory(attached, (*restoring), h, err.Error())
		}
		return
	}
}

func requestSkillHistory(requests *sessioncontrol.Requests[appServerRequest], h *appServerChildHistory, cursor string) error {
	// Omit turnId to scan across turns, including inherited fork items. Only
	// opaque cursors may continue a thread-wide read; item anchors are turn-scoped.
	params := map[string]any{"threadId": h.info.ID, "sortDirection": "desc", "limit": sessionpresentation.HistoryPageSize}
	if cursor != "" {
		params["cursor"] = cursor
	}
	id, err := requests.Send("thread/items/list", params, "main", appServerRequest{label: "activity-skills/" + h.info.ID})
	h.skills.request = id
	return err
}

func failSkillHistory(attached *attachedPresentation, restoring *appServerActivityRestore, h *appServerChildHistory, message string) error {
	h.skills.request, h.skills.names = "", nil
	restoreHistoryNotice(attached, restoring, "Could not restore active skills for "+attached.session.Caller(h.info.ID)+": "+message, false)
	return nil
}

func skillHistoryResponse(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], childKey *scope.Key[*appServerChildHistory], restoring **appServerActivityRestore, parent context.Context, retained changes.Scope, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, thread string, m appserver.Message) error {
	h := childHistory(&attached.session, childKey, thread)
	if h == nil || h.skills == nil || h.skills.request != string(m.ID) {
		return nil
	}
	scan := h.skills
	scan.request = ""
	if m.Error != nil {
		return failSkillHistory(attached, (*restoring), h, m.Error.Message)
	}
	scan.bytes += len(m.Result)
	if scan.bytes > restoredRolloutLimit || len(scan.cursors) >= 1024 {
		return failSkillHistory(attached, (*restoring), h, "Current-context history exceeds the observation limit; the count is unavailable.")
	}
	var page binding.ItemPage
	if err := json.Unmarshal(m.Result, &page); err != nil {
		return failSkillHistory(attached, (*restoring), h, "Invalid item page: "+err.Error())
	}
	for _, entry := range page.Data {
		if entry.TurnID == "" || entry.Item.ID == "" {
			return failSkillHistory(attached, (*restoring), h, "Item page returned no item or turn identity.")
		}
		item := entry.Item
		if item.Type == "contextCompaction" && item.Status != "inProgress" {
			page.NextCursor = ""
			break
		}
		native := &sessionpresentation.NativeItem{Thread: thread, Turn: entry.TurnID, Item: item.ID, Status: item.Status, Phase: "item/completed", Command: item.Command}
		record := sessionpresentation.ActivityRecord{Source: sessionpresentation.ActivityEntry{Kind: "tool", Native: native}}
		switch item.Type {
		case "userMessage":
			record.Source.Kind, native.Item = "attachments", item.ID+"/attachments"
			native.Attachments = sessionpresentation.AttachmentValues(attached.cwd, item.Content)
		case "commandExecution":
			restoreCommandSegments(parent, retained, segments, attached, activityService, (*restoredSegments), &record.Source, item, attached.cwd)
			if item.ExitCode != nil && *item.ExitCode != 0 && len(native.Segments) == 0 {
				continue
			}
			record.Source.Text = sessionpresentation.ToolText(item.Observation(), attached.cwd)
		default:
			continue
		}
		record.Values = sessionpresentation.ActivityContent(record.Source).Values()
		scan.names = scan.names.With(activitypresentation.SkillLoads(record)...)
	}
	if page.NextCursor != "" {
		if scan.cursors[page.NextCursor] {
			return failSkillHistory(attached, (*restoring), h, "Item history returned a repeated pagination cursor.")
		}
		scan.cursors[page.NextCursor] = true
		if err := requestSkillHistory(control.Requests, h, page.NextCursor); err != nil {
			failSkillHistory(attached, (*restoring), h, err.Error())
		}
		return nil
	}
	v, name := attached.childSource, attached.session.Caller(h.info.ID)
	// Observe a live reset before adopting the historical snapshot. Its newer
	// context wins, even if the reset's entry has already left the feed.
	v.RestoreSkills(name, h.skills.names)
	h.skills.names = nil

	return nil
}
