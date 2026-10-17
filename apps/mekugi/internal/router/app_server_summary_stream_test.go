package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotAppServerReasoningPacedSections(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return at }
			u.view.Clock, u.agents.Clock = u.clock, u.clock
			u.view.Painter.Theme, u.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.testActivity(u.agents).setDisplayFilter(false, "/root/worker")
			view := u.testActivity(u.view)
			if thread == "child" {
				view = u.testActivity(u.agents)
			}
			notify := func(method string, params map[string]any) {
				t.Helper()
				params["threadId"], params["turnId"] = thread, "t"
				appServerTestNotify(t, u, method, params)
			}
			snapshot := func(stage string) {
				t.Run(stage, func(t *testing.T) {
					uisnapshot.Assert(t, "testdata/snapshots/reasoning-paced-sections-"+thread+"-"+stage+".txt", strings.Join(view.RenderFeed(80, 40).Lines, "\n")+"\n")
				})
			}
			body := "Inspecting layout\n\nFirst checkpoint.\nSecond checkpoint.\n\n**Checking completion.**\n\nThird checkpoint.\nFourth checkpoint.\n"
			notify("item/started", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning"}})
			notify("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "delta": body})
			if len(view.Entries) != 1 || len(view.RenderFeed(80, 40).Lines) != 0 {
				t.Fatal("summary position was not reserved invisibly before its first frame")
			}
			u.flushStreamOutput()
			snapshot("first-frame")
			if len(view.Entries) != 1 || len(view.blocks(0)) != 1 || view.blocks(0)[0].Body == strings.TrimSpace(body) {
				t.Fatal("burst jumped directly to its full body")
			}
			at = at.Add(2 * time.Second)
			notify("item/completed", map[string]any{"item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
			if !view.blocks(0)[0].Live {
				t.Fatal("completion was not held behind the summary backlog")
			}
			u.flushStreamOutput()
			snapshot("rolling")
			rollCommandOutput(u)
			snapshot("done")
			if view.blocks(0)[0].Collapsed || view.blocks(0)[1].Collapsed || settleActivity(at.Add(time.Minute), view) {
				t.Fatal("latest completed reasoning did not stay open")
			}
			if len(view.blocks(0)) != 2 || view.blocks(0)[0].Live || view.blocks(0)[1].Live || view.blocks(0)[0].Thought != 0 || view.blocks(0)[1].Thought != 2*time.Second {
				t.Fatalf("section completion or item duration lost: %+v", view.Entries)
			}
			// The compact row retains one dialog page per original section.
			nextEvent(t, u, thread)
			settleActivity(at.Add(time.Minute), view)
			snapshot("collapsed")
			feed := view.RenderFeed(80, 40)
			for i := range 2 {
				opened := false
				for _, snippet := range feed.Snippets {
					block, ok := view.SnippetBlock(snippet)
					if !ok || block.Source != view.Entries[0].Source.Seq || len(block.Members) != 2 {
						continue
					}
					if !u.shell.openOutput(view.bind(u.shell), snippet) {
						t.Fatal("section has no dialog target")
					}
					page := view.Painter.DetailDocument(u.shell.output.pages[i].DetailPage, nil, nil, 80)
					if page.Text != block.Members[i].Body || strings.Contains(page.Text, []string{"Third checkpoint.", "First checkpoint."}[i]) {
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
			if len(view.blocks(0)) != 2 {
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
		if len(u.source.Entries) != 0 || len(u.childSource.Entries) != 0 {
			t.Fatal("empty reasoning starts created visible placeholders")
		}
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "interrupted"}})
		u.flushStreamOutput()
		if len(u.source.Entries) != 0 || len(u.childSource.Entries) != 0 {
			t.Fatal("empty reasoning left live state behind")
		}
		for _, id := range []string{"raw-one", "raw-two"} {
			appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": id, "delta": "Late public summary."})
		}
		u.flushStreamOutput()
		if len(u.source.Entries) != 0 || len(u.childSource.Entries) != 0 {
			t.Fatal("late delta reopened interrupted empty reasoning")
		}
	}
}

func TestReasoningSourceCompletesBeforeTerminalReveal(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t"}})
			body := "Inspecting retained facts\n\n" + strings.Repeat("Public checkpoint.\n", 24)
			appServerTestNotify(t, u, "item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "t", "itemId": "r", "delta": body})
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": "r", "type": "reasoning", "summary": []string{body}}})
			source, view := u.source, u.view
			if thread == "child" {
				source, view = u.childSource, u.agents
			}
			if len(source.Entries) != 1 || source.Entries[0].Source.Text != body || source.Entries[0].Source.Native.Phase != "item/completed" {
				t.Fatalf("source completion waited for terminal reveal: %+v", source.Entries)
			}
			if strings.Contains(strings.Join(view.RenderFeed(80, 50).Lines, "\n"), "Public checkpoint") {
				t.Fatal("source completion bypassed terminal reveal")
			}
			revision, sequence := source.Revision, source.LastSeq
			for range 20 {
				u.flushSummaryOutput()
				u.publishActivity()
			}
			if source.Revision != revision || source.LastSeq != sequence {
				t.Fatal("terminal reveal revised semantic source state")
			}
			if !strings.Contains(strings.Join(view.RenderFeed(80, 50).Lines, "\n"), "Public checkpoint") {
				t.Fatal("terminal reveal did not reach admitted content")
			}
		})
	}
}
