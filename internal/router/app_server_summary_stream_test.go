package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotAppServerReasoningPacedSections(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return at }
			u.view.clock, u.agents.clock = u.clock, u.clock
			u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.agents.only, u.agents.selected = false, "/root/worker"
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			notify := func(method string, params map[string]any) {
				t.Helper()
				params["threadId"], params["turnId"] = thread, "t"
				appServerTestNotify(t, u, method, params)
			}
			snapshot := func(stage string) {
				t.Run(stage, func(t *testing.T) {
					uisnapshot.Assert(t, "testdata/snapshots/reasoning-paced-sections-"+thread+"-"+stage+".txt", strings.Join(view.renderFeed(80, 40).lines, "\n")+"\n")
				})
			}
			body := "Inspecting layout\n\nFirst checkpoint.\nSecond checkpoint.\n\n**Checking completion.**\n\nThird checkpoint.\nFourth checkpoint.\n"
			notify("item/started", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning"}})
			notify("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "delta": body})
			if len(view.entries) != 1 || len(view.renderFeed(80, 40).lines) != 0 {
				t.Fatal("summary position was not reserved invisibly before its first frame")
			}
			u.flushStreamOutput()
			snapshot("first-frame")
			if len(view.blocks) != 1 || len(view.blocks[0]) != 1 || view.blocks[0][0].Body == strings.TrimSpace(body) {
				t.Fatal("burst jumped directly to its full body")
			}
			at = at.Add(2 * time.Second)
			notify("item/completed", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
			if run := u.session.summaries[[3]string{thread, "t", "r"}]; run == nil || run.done == nil {
				t.Fatal("completion was not held behind the summary backlog")
			}
			u.flushStreamOutput()
			snapshot("rolling")
			rollCommandOutput(u)
			snapshot("done")
			if len(u.session.summaries) != 0 || len(view.blocks[0]) != 2 || view.blocks[0][0].Live || view.blocks[0][1].Live || view.blocks[0][0].Elapsed != "" || view.blocks[0][1].Elapsed != "2s" {
				t.Fatalf("section completion or item duration lost: %+v", view.blocks)
			}
			if settleActivity(at.Add(time.Hour), view) {
				t.Fatal("summary collapsed before a later event")
			}
			nextEvent(t, u, thread)
			if settleActivity(at, view) {
				t.Fatal("summary collapsed before events paused")
			}
			at = at.Add(activityui.OutputDebounce)
			if !settleActivity(at, view) {
				t.Fatal("summary did not collapse with the output debounce")
			}
			snapshot("collapsed")
			// Each title opens only its own retained section, not the whole item.
			feed := view.renderFeed(80, 40)
			for i := range 2 {
				opened := false
				for _, snippet := range feed.snippets {
					block, ok := view.snippetBlock(snippet)
					if !ok || block.Source != view.entries[0].Seq || block.Section != i {
						continue
					}
					if !u.shell.openOutput(view, snippet) {
						t.Fatal("section has no dialog target")
					}
					page := view.painter.DialogPage(u.shell.output.pages[0], 80)
					if page.Text != block.Body || strings.Contains(page.Text, []string{"Third checkpoint.", "First checkpoint."}[i]) {
						t.Fatalf("section dialog mixed bodies: %+v", page)
					}
					opened = true
					break
				}
				if !opened {
					t.Fatal("section has no rendered click target")
				}
			}
			notify("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "delta": "Late text."})
			rollCommandOutput(u)
			if len(u.session.thinking) != 0 || len(u.session.summaries) != 0 || len(view.blocks[0]) != 2 {
				t.Fatal("late delta reopened completed reasoning")
			}
		})
	}
}

func TestAppServerEmptyReasoningStartsDoNotCreateRows(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
		for _, id := range []string{"raw-one", "raw-two"} {
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": id, "type": "reasoning"}})
		}
		u.flushStreamOutput()
		if len(u.view.entries) != 0 || len(u.agents.entries) != 0 {
			t.Fatal("empty reasoning starts created visible placeholders")
		}
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "interrupted"}})
		u.flushStreamOutput()
		if len(u.session.thinking) != 0 || len(u.session.summaries) != 0 {
			t.Fatal("empty reasoning left live state behind")
		}
		for _, id := range []string{"raw-one", "raw-two"} {
			appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": id, "delta": "Late public summary."})
		}
		u.flushStreamOutput()
		if len(u.session.thinking) != 0 || len(u.session.summaries) != 0 || len(u.view.entries) != 0 || len(u.agents.entries) != 0 {
			t.Fatal("late delta reopened interrupted empty reasoning")
		}
	}
}
