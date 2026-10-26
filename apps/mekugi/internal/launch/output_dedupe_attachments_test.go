package launch

import (
	json "encoding/json/v2"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestUISnapshotOutputDeduplicationAttachments(t *testing.T) {
	var file, skill string
	for i := range 600 {
		file += fmt.Sprintf("file row %04d with distinctive submitted snapshot content\n", i)
	}
	for i := range 20 {
		skill += fmt.Sprintf("skill instruction %04d with independent retained content\n", i)
	}
	// The first file spans multiple frames. The second has a new final line;
	// the third is identical to the second but has not entered the request yet.
	var input []any
	var retained []binding.ThreadItem
	var snapshots []attachments.Snapshot
	for i, body := range []string{file, file + "new final line\n", file + "new final line\n"} {
		u, w := newAppServerTestUI()
		frames := append(composer.FileFrames("/workspace/file.txt", body), composer.SkillFrames("guide", "", skill)...)
		draft := conversationmodel.Input{Text: "review snapshots", Attachments: []string{attachments.Encode(frames)}}
		if err := sendConversationInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, []conversationmodel.Input{draft}, false); err != nil {
			t.Fatal(err)
		}
		sent := appServerTurnRequests(t, w)
		if len(sent) != 1 {
			t.Fatalf("submission requests: %d", len(sent))
		}
		snapshot, ok := attachments.DecodeSnapshot(sent[0].Params.Input[1].Text)
		if !ok || snapshot.ID != sent[0].Params.ClientUserMessageID+"/0" || len(snapshot.Frames) < 3 {
			t.Fatalf("missing durable chunked submission identity: %+v", snapshot)
		}
		snapshots = append(snapshots, snapshot)
		content, _ := json.Marshal(sent[0].Params.Input)
		retained = append(retained, binding.ThreadItem{Type: "userMessage", ID: fmt.Sprintf("user-%d", i), Content: content})
		var parts []any
		for _, part := range sent[0].Params.Input {
			parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
		}
		input = append(input, map[string]any{"role": "user", "content": parts, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "same-turn"}})
	}
	if snapshots[0].ID == snapshots[1].ID || snapshots[1].ID == snapshots[2].ID {
		t.Fatal("equal submissions reused identity")
	}
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", child), func(t *testing.T) {
			v := sharedEventsView(child)
			main := "thread"
			if child {
				main = "parent"
			}
			for _, item := range retained {
				applyTranscriptFixture(v, true, "/workspace", main, "thread", "same-turn", item.ID, "item/started", "", item)
			}
			proxy := newManagedMekugiProxy(t)
			proxy.inputProjections = testInputProjections()
			proxy.activity.Attach("thread")
			request := duplicateTestPrepare(t, proxy, input[:2], codexresponses.TurnMetadata{})
			if !responses.SameJSONValue(request.OriginalField("input"), responses.MustMarshalJSON(input[:2])) {
				t.Fatal("original attachment input changed")
			}
			if !strings.Contains(string(request.Fields["input"]), "See O") {
				t.Fatal("attachment body not deduplicated")
			}
			// Identity must survive model-only frame replacement before wire expansion.
			projected := duplicateTestItems(t, request)
			var parts []struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(projected[1]["content"], &parts)
			projectedSnapshot, ok := attachments.DecodeSnapshot(parts[1].Text)
			if !ok || projectedSnapshot.ID != snapshots[1].ID {
				t.Fatal("projection replaced submission identity")
			}
			requestexecution.ProjectFileAttachments(request)
			if strings.Contains(string(request.Fields["input"]), "mekugi-file-attachments-v") {
				t.Fatal("durable envelope leaked to model")
			}
			events := takeNativeActivity(proxy.activity, "thread")
			if len(events) != 2 {
				t.Fatalf("got %d repetition receipts, want one file and one skill", len(events))
			}
			apply := func(view *activityAdapter, events []sessionpresentation.ActivityEntry) {
				for _, event := range events {
					event.Seq = view.LastSeq + 1
					view.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{event}})
				}
			}
			apply(v, events)
			// Completion refresh must retain each snapshot's own annotation.
			for _, item := range retained {
				applyTranscriptFixture(v, true, "/workspace", main, "thread", "same-turn", item.ID, "item/completed", "", item)
			}
			check := func(view *activityAdapter) {
				t.Helper()
				if len(view.Entries) != 6 {
					t.Fatalf("duplicate or missing snapshot receipts: %d entries", len(view.Entries))
				}
				for i := range 3 {
					blocks := view.blocks(i*2 + 1)
					if len(blocks) != 2 {
						t.Fatalf("snapshot %d has %d rows", i, len(blocks))
					}
					want := []string{"", ""}
					if i == 1 {
						want = []string{"600 lines repeated", "repeated"}
					}
					for j := range blocks {
						if blocks[j].Repeated != want[j] {
							t.Fatalf("snapshot %d row %d: repetition %q, want %q", i, j, blocks[j].Repeated, want[j])
						}
					}
					wantFile := file
					if i > 0 {
						wantFile += "new final line\n"
					}
					if view.Painter.DialogPage(blocks[0], 80).Text != wantFile || view.Painter.DialogPage(blocks[1], 80).Text != skill {
						t.Fatal("repetition changed original attachment details")
					}
				}
			}
			check(v)
			// The producer can finish projection before the native user item arrives.
			// Deliver its event to an empty feed, then hydrate the item from host history.
			delayed, _ := newAppServerTestUI()
			delayed.audience, delayed.clock = proxy.activity, v.Clock
			replaceAppServerTestSession(t, delayed, "thread")
			delayedView := delayed.testActivity(delayed.shell.Transcript)
			delayedView.Conversation = true
			delayedView.testAudience()
			apply(delayedView, events)
			if len(delayedView.Entries) != 0 {
				t.Fatal("early repetition created a transcript row")
			}
			history := []binding.HistoryTurn{{ID: "same-turn", Status: "completed", Items: retained}}
			delayed.restoreHistory(history)
			check(delayedView)
			if got := ansi.Strip(strings.Join(delayedView.RenderFeed(80, 40).Lines, "\n")); !strings.Contains(got, "600 lines repeated") || !strings.Contains(got, "guide · repeated") {
				t.Fatalf("late item lost rendered suffix: %s", got)
			}
			if !child {
				assertNativeUISnapshot(t, "output-dedupe-attachments-restored", delayedView.RenderFeed(80, 40).Lines)
			}
			// Real attachment replacement must neither leak the receipt into another
			// root nor need another provider round to recover it on switching back.
			oldFeed := delayed.attached.source.Model
			replaceAppServerTestSession(t, delayed, "other")
			if delayed.attached.source.Model == oldFeed {
				t.Fatal("fixture did not replace the feed")
			}
			delayed.restoreHistory(history)
			for _, entry := range delayed.attached.source.Entries {
				for _, value := range entry.Values {
					if value.Repeated != "" {
						t.Fatal("foreign root inherited repetition")
					}
				}
			}
			replaceAppServerTestSession(t, delayed, "thread")
			delayed.restoreHistory(history)
			check(delayed.testActivity(delayed.shell.Transcript))
			if events := takeNativeActivity(proxy.activity, "thread"); len(events) != 0 {
				t.Fatal("switch republished whole-history supplements")
			}
			duplicateTestPrepare(t, proxy, input[:2], codexresponses.TurnMetadata{})
			if events := takeNativeActivity(proxy.activity, "thread"); len(events) != 0 {
				t.Fatal("refreshed request duplicated receipts")
			}
			assertNativeUISnapshot(t, fmt.Sprintf("output-dedupe-attachments-child-%t", child), v.RenderFeed(80, 40).Lines)
			// Fresh restore has only durable host items, not a live parent's state.
			restored, _ := newAppServerTestUI()
			restored.Thread = "thread"
			restored.startSession("thread", "/workspace")
			restored.restoreHistory([]binding.HistoryTurn{{ID: "same-turn", Status: "completed", Items: retained}})
			restoredView := restored.testActivity(restored.shell.Transcript)
			fresh := newManagedMekugiProxy(t)
			fresh.inputProjections = testInputProjections()
			fresh.activity.Attach("thread")
			duplicateTestPrepare(t, fresh, input[:2], codexresponses.TurnMetadata{})
			apply(restoredView, takeNativeActivity(fresh.activity, "thread"))
			check(restoredView)
			// Both identical submissions can coexist in the same turn. Each
			// must retain its own two receipts, rather than sharing occurrence state.
			both := newManagedMekugiProxy(t)
			both.inputProjections = testInputProjections()
			both.activity.Attach("thread")
			duplicateTestPrepare(t, both, input, codexresponses.TurnMetadata{})
			bothEvents := takeNativeActivity(both.activity, "thread")
			if len(bothEvents) != 4 {
				t.Fatalf("identical submissions produced %d receipts", len(bothEvents))
			}
			for _, snapshot := range snapshots[1:] {
				for _, frame := range []string{snapshot.Frames[0], snapshot.Frames[len(snapshot.Frames)-1]} {
					if !slices.ContainsFunc(bothEvents, func(event sessionpresentation.ActivityEntry) bool { return event.CallID == snapshot.ReceiptID(frame) }) {
						t.Fatal("identical submission lost its own receipt")
					}
				}
			}
			// The first identical submission may disappear from model context while
			// its transcript remains. Only the explicitly identified survivor is marked.
			compacted := append(duplicateTestInput("read original", "source", file), input[2])
			isolated := newManagedMekugiProxy(t)
			isolated.inputProjections = testInputProjections()
			isolated.activity.Attach("thread")
			duplicateTestPrepare(t, isolated, compacted, codexresponses.TurnMetadata{})
			events = takeNativeActivity(isolated.activity, "thread")
			if len(events) != 1 || events[0].CallID != snapshots[2].ReceiptID(snapshots[2].Frames[0]) {
				t.Fatalf("omitted predecessor confused receipt attribution: %+v", events)
			}
		})
	}
}
