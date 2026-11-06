package launch

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeDeliveredTrafficSurvivesResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		live := newAppServerSessionTestUI(t, t.TempDir())
		wire := live.Host.Input.(*appServerTestInput)
		var records []map[string]any
		bodies := []string{"## Summary\nMESSAGE result.\n\n## Details\nPayload:\nMessage evidence.\n", "## Summary\nFINAL_ANSWER result.\n\n## Details\nPayload:\nFinal evidence.\n", "Earlier progress must not replay.\n", "## Summary\nFollow-up result.\n\n## Details\nFollow-up evidence.\n"}
		for i, kind := range []string{"MESSAGE", "FINAL_ANSWER", "FINAL_ANSWER", "FINAL_ANSWER"} {
			input := control.NativeInput{Thread: "main", ID: fmt.Sprint(i), Sender: "/root/worker", Recipient: "/root", Kind: kind, Message: bodies[i], SourceGeneration: uint64(i + 1), ParentTurn: fmt.Sprint("turn-", i)}
			if i == 2 {
				input.ID, input.Outcome = "interrupted", control.Interrupted
			}
			if kind == "FINAL_ANSWER" {
				input.Message += completionTestTrailer
			}
			wire.Reset()
			result := make(chan error, 1)
			go func() { _, err := live.Requests.Input(t.Context(), input); result <- err }()
			synctest.Wait()
			live.Requests.Dispatch(<-live.Calls())
			var sent struct {
				ID     jsontext.Value `json:"id"`
				Params struct {
					Items []map[string]any `json:"items"`
				} `json:"params"`
			}
			if err := json.Unmarshal(wire.Bytes(), &sent); err != nil {
				t.Fatal(err)
			}
			request, _ := live.Requests.Lookup(string(sent.ID))
			if !live.Requests.CompleteCall(appserver.Message{ID: sent.ID, Result: []byte(`{}`)}) {
				t.Fatal("delivery acknowledgement missing")
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			publishNativeDelivery(&live.attached, *request.NativeDelivery(), time.Now())
			entry := live.attached.source.Entries[len(live.attached.source.Entries)-1].Source
			if entry.Text != bodies[i] || entry.Message.Text != bodies[i] || request.NativeDelivery().Message != input.Message {
				t.Fatalf("delivery %s: entry=%q message=%q retained=%q want=%q", input.ID, entry.Text, entry.Message.Text, request.NativeDelivery().Message, bodies[i])
			}
			if live.attached.source.Entries[len(live.attached.source.Entries)-1].Source.ActivitySeq == 0 {
				t.Fatalf("live %s delivery lost its child-feed link", kind)
			}
			if entry.ActivityFacts().Suppressed != (input.Outcome == control.Interrupted) {
				t.Fatal("interrupted delivery remained visible or hid ordinary traffic")
			}
			// Both deliveries arrive while idle, one before any host turn.
			record := rolloutRecord(i*3+1, "response_item", sent.Params.Items[0])
			records = append(records, record, record) // Repeated retained identity must coalesce.
			if i == 0 {
				records = append(records,
					rolloutRecord(2, "event_msg", map[string]any{"type": "task_started", "turn_id": "saved"}),
					rolloutCompleted(3, "saved", "answer"),
					rolloutRecord(3, "event_msg", map[string]any{"type": "task_complete", "turn_id": "saved"}))
			}
			// An ordinary assistant's printed envelope supplies no delivery provenance.
			lookalike := map[string]any{"type": "message", "id": control.MessageItemID("lookalike-" + input.ID), "role": "assistant", "phase": "commentary", "content": sent.Params.Items[0]["content"]}
			records = append(records, rolloutRecord(i*3+1, "response_item", lookalike))
		}
		liveRendered := ansi.Strip(strings.Join(live.testActivity(live.shell.Transcript).testRenderConversation(100).Lines, "\n"))
		if strings.Count(liveRendered, "finished") != 2 || strings.Count(liveRendered, "├─✓ finished") != 1 {
			t.Fatalf("live completions nested or lost:\n%s", liveRendered)
		}
		for _, idle := range []bool{false, true} {
			restored := newAppServerSessionTestUI(t, live.attached.cwd)
			root := binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd, Path: writeTestRollout(t, "main", records...)}
			want := []string{bodies[0], "host answer", bodies[1], bodies[3]}
			if idle {
				root.Path = writeTestRollout(t, "main", records[0], records[1])
				want = want[:1]
			} else {
				root.Turns = []binding.HistoryTurn{{ID: "saved", Status: "completed", Items: []binding.ThreadItem{{ID: "answer", Type: "agentMessage", Text: "host answer"}}}}
			}
			if err := restored.restoreContent(root); err != nil {
				t.Fatal(err)
			}
			var got []string
			var target uint64
			for _, entry := range restored.attached.source.Entries {
				if entry.Source.ActivityFacts().Suppressed {
					if entry.Source.Text != bodies[2] {
						t.Fatal("restoration suppressed ordinary traffic")
					}
					continue
				}
				got = append(got, entry.Source.Text)
				if entry.Source.Text == "host answer" {
					continue
				}
				if entry.Source.Agent != "/root/worker" || entry.Source.Native == nil || entry.Source.Native.Thread != "main" {
					t.Fatalf("delivery lost attribution: %+v", entry.Source)
				}
				if entry.Source.Kind != "reply" && entry.Source.Kind != "final" {
					t.Fatalf("delivery lost message/result kind: %+v", entry.Source)
				}
				if entry.Source.Kind == "reply" && (entry.Source.Message == nil || entry.Source.Message.From != "/root/worker" || entry.Source.Message.To != "Main") {
					t.Fatalf("message lost receiver presentation: %+v", entry.Source)
				}
				if entry.Source.ActivitySeq == 0 || entry.Values[0].ReplySummary == "" {
					t.Fatal("restored reply lost its summary or exact Activity target")
				}
				target = entry.Source.Seq
			}
			if !slices.Equal(got, want) {
				t.Fatalf("idle=%v: restored traffic = %q, want %q", idle, got, want)
			}
			view := restored.testActivity(restored.shell.Transcript)
			rendered := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
			if !idle && (strings.Count(rendered, "worker → main finished") != 1 || strings.Contains(rendered, "├─✓ worker → main finished") || strings.Count(rendered, "Follow-up result.") != 1) {
				t.Fatalf("restored completions nested or lost:\n%s", rendered)
			}
			if strings.Contains(rendered, bodies[2]) {
				t.Fatalf("restoration replayed interrupted traffic: %s", rendered)
			}
			if !strings.Contains(rendered, "MESSAGE result.") || !strings.Contains(rendered, "↩ Open reply") || strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details") || strings.Contains(rendered, "Message evidence.") {
				t.Fatalf("restored message disclosure: %s", rendered)
			}
			if !idle && (!strings.Contains(rendered, "FINAL_ANSWER result.") || !strings.Contains(rendered, "↩ Open reply") || strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details") || strings.Contains(rendered, "Final evidence.")) {
				t.Fatalf("restored default disclosure: %s", rendered)
			}
			view.Expansion = 2
			view.InvalidateRuns(0)
			rendered = ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
			if !idle && (strings.Count(rendered, "FINAL_ANSWER result.") != 1 || !strings.Contains(rendered, "Final evidence.") || strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details")) {
				t.Fatalf("restored expanded disclosure: %s", rendered)
			}
			if err := restored.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
				t.Fatal(err)
			}
			if !terminalTest(restored).openActivityReply(target) {
				t.Fatal("restored reply cannot open its exact Activity target")
			}
			if err := restored.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
				t.Fatal(err)
			}
			if terminalTest(restored).Dialog.Document.Text != want[len(want)-1] {
				t.Fatal("restored reply lost its original body")
			}
			if requests := restoreContentRequests(t, restored.Host.Input.(*appServerTestInput)); len(requests) != 0 {
				t.Fatalf("restoration dispatched host requests: %+v", requests)
			}
		}
	})
}

// This is the reported shape: an old parent assignment, two later direct child
// inputs, and two distinct completions delivered back to the parent.
func TestUISnapshotDeliveredCompletionContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		live := newAppServerSessionTestUI(t, t.TempDir())
		direct := newAppServerSessionTestUI(t, t.TempDir())
		start := time.Date(2026, 11, 6, 17, 32, 33, 0, time.Local)
		live.clock = func() time.Time { return start }
		appServerTestNotify(t, live, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "fix-attachments-540-astra"}})
		peer := live.attached.session.Caller("child")
		assignment := coordinationTestCall(t, "old-task", "followup_task", "child", peer, "Your handoff says incomplete. Continue through validation, review and commit.")
		gap := binding.ThreadItem{ID: "gap", Type: "agentMessage", Text: "Waiting for the child."}
		for _, item := range []binding.ThreadItem{assignment, gap} {
			appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "main", "turnId": "parent-turn", "item": item})
		}
		bodies := []string{
			"No. Actual `@` file attachments and `$` skill attachments retain their existing framing and behavior. This change targets selected screen text.\n\nUseful framing includes XML boundaries and a diff fence.\nThe current commit does not yet add those wrappers.",
			"## Summary\nImplemented XML boundaries for selected text and `diff` fences for selected diffs.\n\n## Details\nBoth initial delivery and recall preserve framing. Validation passed.",
		}
		questions := []string{"Does it affect actual file attachments? We still need XML framing and a diff wrapper.", "yes"}
		times := []time.Time{start.Add(18*time.Minute + 34*time.Second), start.Add(40*time.Minute + time.Second)}
		wire := live.Host.Input.(*appServerTestInput)
		var records []map[string]any
		for i, body := range bodies {
			turn := fmt.Sprintf("direct-child-turn-%d", i)
			appServerTestNotify(t, direct, "item/completed", map[string]any{"threadId": "main", "turnId": turn,
				"item": map[string]any{"id": "input-" + turn, "type": "userMessage", "content": []map[string]string{{"type": "text", "text": questions[i]}}}})
			appServerTestNotify(t, direct, "item/completed", map[string]any{"threadId": "main", "turnId": turn,
				"item": map[string]any{"id": "answer-" + turn, "type": "agentMessage", "phase": "final_answer", "text": body}})
			input := control.NativeInput{Thread: "main", ID: turn, Sender: peer, Recipient: "/root", Kind: "FINAL_ANSWER", Message: body, Outcome: control.Completed, SourceGeneration: uint64(i + 5), ParentTurn: turn}
			wire.Reset()
			result := make(chan error, 1)
			go func() { _, err := live.Requests.Input(t.Context(), input); result <- err }()
			synctest.Wait()
			live.Requests.Dispatch(<-live.Calls())
			var sent struct {
				ID     jsontext.Value `json:"id"`
				Params struct {
					Items []map[string]any `json:"items"`
				} `json:"params"`
			}
			if err := json.Unmarshal(wire.Bytes(), &sent); err != nil {
				t.Fatal(err)
			}
			request, _ := live.Requests.Lookup(string(sent.ID))
			live.Requests.CompleteCall(appserver.Message{ID: sent.ID, Result: []byte(`{}`)})
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			publishNativeDelivery(&live.attached, *request.NativeDelivery(), times[i])
			records = append(records, map[string]any{"timestamp": times[i].Format(time.RFC3339Nano), "type": "response_item", "payload": sent.Params.Items[0]})
		}
		for _, restored := range []bool{false, true} {
			u := live
			if restored {
				u = newAppServerSessionTestUI(t, live.attached.cwd)
				if err := u.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", records...), Turns: []binding.HistoryTurn{{ID: "parent-turn", Status: "completed", Items: []binding.ThreadItem{assignment, gap}}}}); err != nil {
					t.Fatal(err)
				}
			}
			view := u.testActivity(u.shell.Transcript)
			feed := view.testRenderConversation(120)
			plain := ansi.Strip(strings.Join(feed.Lines, "\n"))
			if strings.Count(plain, "Your handoff says incomplete.") != 1 {
				t.Fatalf("fixture lost its obsolete parent assignment:\n%s", plain)
			}
			if strings.Count(plain, "fix-attachments-540-astra → main finished") != 1 || !strings.Contains(plain, "├─✓ finished · 21m27s") || strings.Contains(plain, "re: assignment") || strings.Count(plain, "No. Actual") != 1 || strings.Count(plain, "Implemented XML") != 1 {
				t.Fatalf("restored=%t: delivered completion context/continuation regressed:\n%s", restored, plain)
			}
			// Snapshot just the reported completed exchange, leaving preceding
			// assignment/history timing outside the visual reproduction.
			first := slices.IndexFunc(feed.Lines, func(line string) bool {
				return strings.Contains(ansi.Strip(line), "fix-attachments-540-astra → main finished")
			})
			assertNativeUISnapshot(t, "delivered-completion-context", feed.Lines[first:])
			for _, entry := range u.attached.source.Entries {
				if entry.Source.Kind != "final" {
					continue
				}
				if !terminalTest(u).openActivityReply(entry.Source.Seq) {
					t.Fatal("completion lost exact reply navigation")
				}
				if err := u.testPaint(&bytes.Buffer{}, 120, 30); err != nil {
					t.Fatal(err)
				}
				if terminalTest(u).Dialog.Document.Text != entry.Source.Text {
					t.Fatal("completion navigated to another answer")
				}
			}
			child := u.testActivity(u.shell.Agents)
			child.setDisplayFilter(true, peer)
			child.Conversation = true
			childText := ansi.Strip(strings.Join(child.RenderFeed(120, 80).Lines, "\n"))
			if strings.Count(childText, "No. Actual") != 1 || strings.Count(childText, "Implemented XML") != 1 || strings.Contains(childText, "re: assignment") {
				t.Fatalf("restored=%t: child consumer lost answers or guessed assignment:\n%s", restored, childText)
			}
		}
		directText := ansi.Strip(strings.Join(direct.testActivity(direct.shell.Transcript).RenderFeed(120, 80).Lines, "\n"))
		if !strings.Contains(directText, "Does it affect actual file attachments?") || !strings.Contains(directText, "yes") || !strings.Contains(directText, "No. Actual") || !strings.Contains(directText, "Implemented XML") {
			t.Fatalf("direct child inputs and answers missing from their own transcript:\n%s", directText)
		}
	})
}
