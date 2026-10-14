package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotWaitJournalOrder(t *testing.T) {
	for _, yielded := range []bool{false, true} {
		name := "waiting"
		if yielded {
			name = "yielded"
		}
		t.Run(name, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			u, launch := orchestrateIdentityPendingTurnInWorkspace(t, proxy.replayStore, workspace)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
			transform.proxy, transform.shellThreadID, transform.shellTurnID = u.proxy, "main", "waiting-turn"
			now := time.Date(2026, 10, 14, 8, 0, 0, 0, time.Local)
			u.clock, u.view.clock, u.agents.clock = func() time.Time { return now }, func() time.Time { return now }, func() time.Time { return now }
			u.view.painter.Theme = livediff.DarkTheme
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "older-turn", "item": appServerItem{ID: "older-wait", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "completed", Result: mustMarshalJSON(map[string]any{"structuredContent": map[string]any{"state": "canceled"}})}})
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"waiting-turn"}}}`)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "waiting-turn", "item": appServerItem{ID: "prompt", Type: "userMessage", Content: mustMarshalJSON([]map[string]string{{"type": "text", "text": "Describe the sample checks"}})}})
			if err := u.proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "main", "waiting-turn"); err != nil {
				t.Fatal(err)
			}
			if _, err := u.proxy.applyJournal(t.Context(), workspace, "main", "update", []journalMutation{{Op: "add", Kind: "note", Title: new("Verified the journal update"), Body: new("Same-turn work report")}}); err != nil {
				t.Fatal(err)
			}
			if err := u.proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, "main", func(j *threadJournal, _ bool) error {
				for i := range j.Events {
					j.Events[i].At = now.Format(time.RFC3339Nano)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			u.journal = u.proxy.journals.attachNative(workspace, "main")
			t.Cleanup(func() { u.proxy.journals.detachNative(u.journal) })
			if err := u.proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
				t.Fatal(err)
			}
			command := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "wait_agent", callID: "wait", waitTurn: "waiting-turn", waitItem: "wait-item", waitTargets: []string{"batch"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			registered := <-command.reply
			if registered.err != nil {
				t.Fatal(registered.err)
			}
			item := appServerItem{ID: "wait-item", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "completed", Arguments: mustMarshalJSON(map[string]any{"targets": command.waitTargets}), Result: mustMarshalJSON(map[string]any{"structuredContent": registered.wait})}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "waiting-turn", "item": item})
			if yielded {
				u.publishOrchestratedEvent(u.orchestrateThreads["child"], orchestrateEvent{Task: "batch", Kind: "message", Message: "wake"})
				drainOrchestrateWork(t, u)
				orchestrateTestReply(t, u, btwTestRequest(t, u.client.Input.(*appServerTestInput), "turn/start", "main"), `{}`)
			}
			if _, err := u.proxy.applyJournal(t.Context(), workspace, "main", "late-update", []journalMutation{{Op: "add", Kind: "note", Title: new("Late journal milestone"), Body: new("Same-turn update after wait registration")}}); err != nil {
				t.Fatal(err)
			}
			// Fix the mutation timestamp before publication reaches the renderer.
			for _, p := range u.journal.snapshot() {
				if p.event != nil {
					p.event.At = now.Format(time.RFC3339Nano)
				}
			}
			// Terminal publication follows the already-observed wait, just as it
			// does when the host closes the response after registering suspension.
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "waiting-turn", "item": appServerItem{ID: "raw-empty", Type: "agentMessage"}})
			transform.journalFinishRequested = true
			output, err := transform.TransformJSON(regressionFinalResponse(t, "empty", ""))
			if err != nil {
				t.Fatal(err)
			}
			transform.Delivered(output)
			transform.ReleaseDelivery()
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "waiting-turn", "item": appServerItem{ID: "raw-empty", Type: "agentMessage", Phase: "final_answer"}})
			orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"waiting-turn","status":"completed"}}}`)
			rows, _ := u.mainFrame(100, 24, 0)
			visible := strings.Join(rows, "\n")
			if strings.Contains(visible, "re: your message") {
				t.Fatalf("journal replacement left an empty reply:\n%s", visible)
			}
			wait := "Waiting for agent"
			if yielded {
				wait = "Wait yielded"
			}
			card, waiting := strings.Index(visible, "This turn"), strings.Index(visible, wait)
			if card < 0 || waiting <= card {
				t.Fatalf("same-turn journal must precede %s:\n%s", wait, visible)
			}
			if late := strings.LastIndex(visible, "Late journal milestone"); late < 0 || late >= waiting {
				t.Fatalf("same-turn journal update must precede %s:\n%s", wait, visible)
			}
			assertNativeUISnapshot(t, "wait-journal-order-"+name, rows)

			painted := u.journal.snapshot()
			u.journal.startAcknowledgement(t.Context(), u.proxy, painted)
			if err := u.journal.finishAcknowledgement(true); err != nil {
				t.Fatal(err)
			}
			cardIndex := -1
			for i, entry := range u.view.entries {
				if entry.journalCard != nil {
					cardIndex = i
				}
			}
			if cardIndex < 0 {
				t.Fatal("missing published journal card")
			}
			count := liveActivityFeedLimit - len(u.view.entries) + cardIndex + 1
			for i := range count {
				u.view.applyAppServerItem(true, workspace, "main", "main", "later-turn", fmt.Sprintf("later-%d", i), "item/completed", "", appServerItem{Type: "agentMessage", Text: "Sample retained detail"})
			}
			retained := false
			for _, entry := range u.view.entries {
				if entry.journalCard != nil {
					t.Fatal("retention did not retire the report card")
				}
				if entry.native != nil && entry.native.item == "raw-empty" {
					retained = true
					if u.view.visible(entry.activityPaneEntry) {
						t.Fatal("retention revived the replaced empty reply")
					}
				}
			}
			if !retained || strings.Contains(strings.Join(u.view.renderConversation(100).lines, "\n"), "re: your message") {
				t.Fatal("retention lost the source record or rendered its empty reply")
			}
		})
	}
}
