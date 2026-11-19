package launch

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestSessionUIReplayNativeSpawnFollowupAndSideAnswer(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			start := map[string]any{"type": "SubAgentActivity", "kind": "started", "id": "spawn", "agent_path": "/root/worker", "model": "gpt-6-sol", "assignment": "Read the parser."}
			spawn := replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1, start)
			if legacy {
				start["type"], start["event_id"] = "sub_agent_activity", "spawn"
				delete(start, "id")
				spawn = replayTestRecord("event_msg", replayTestEpoch+1, start)
			}
			mail := map[string]any{"type": "agent_message", "id": "followup", "author": "/root", "recipient": "/root/worker", "content": []map[string]string{{"type": "input_text", "text": "Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nReport the result."}}}
			raw := replayTestRecord("event_msg", replayTestEpoch+2, map[string]any{"type": "raw_response_item", "item": mail})
			side := replayTestItem("main", "turn", replayTestEpoch+2, replayTestEpoch+3, map[string]any{"type": "Extension", "kind": "collaboration.side_question", "id": "question", "target": "/root/worker", "question": "Is the parser ready?", "answer": "The parser is ready."})
			path := replayTestWrite(t, t.TempDir(), "main.jsonl", replayTestMeta("main"), spawn, raw, raw, side)
			source, err := replayReadTest(t, nil, t.Context(), path, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
			t.Cleanup(p.close)
			if err := p.advance(p.Until); err != nil {
				t.Fatal(err)
			}
			painted := replayPlaybackTestPaint(t, p, 110, 60)
			for _, body := range []string{"Read the parser.", "Report the result.", "Is the parser ready?", "The parser is ready."} {
				if strings.Count(painted, body) != 1 {
					t.Errorf("%q missing or repeated in replay:\n%s", body, painted)
				}
			}
		})
	}
}

func TestSessionUIReplayNativeFactsMatchLivePresentation(t *testing.T) {
	for _, carrier := range []string{"event", "function_call_output", "custom_tool_call_output"} {
		t.Run(carrier, func(t *testing.T) {
			directory := t.TempDir()
			live := newAppServerSessionTestUI(t, directory)
			target := appserver.CoordinationTarget{ThreadID: "worker", TaskName: "/root/worker", WorkID: "target-work"}
			requested := appserver.CoordinationProjection{Interrupt: &appserver.CoordinationInterruptRequest{OperationID: "interrupt", Target: target}}
			body := "Frozen final evidence.\nCaptured changes: amber1..amber3\nStatus: failed is body text."
			facts := appserver.CoordinationProjection{
				Deliveries: []appserver.CoordinationTerminalDelivery{
					{DeliveryID: "interrupted", Source: target, Disposition: "interrupted", Recipient: "/root", Body: "Earlier progress must not replay."},
					{DeliveryID: "final", Source: appserver.CoordinationTarget{ThreadID: "reviewer", TaskName: "/root/reviewer", WorkID: "review-work"}, Disposition: "completed", Recipient: "/root", Body: body, CaptureResourceRef: new("capture:exact")},
				},
				Wait: &appserver.CoordinationWaitSettlement{
					WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", ClaimID: new("claim"), Mode: "all",
					Origin:   appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "wait-turn", ItemID: "exec", WindowID: "window", CellID: "cell"},
					Selected: []appserver.CoordinationTarget{target}, Result: appserver.CoordinationWaitResult{Kind: "released", Wake: "events"},
					Outcomes: []appserver.CoordinationWaitOutcome{{Target: target, Disposition: "interrupted"}},
					Messages: []appserver.CoordinationWaitMessage{{DeliveryID: "foreign", SenderThreadID: new("foreign-thread"), SenderTaskName: new("/remote/observer"), Recipient: "/root", Body: "Exact foreign message"}},
				},
			}
			records := []map[string]any{replayTestMeta("main")}
			emit := func(second int64, projection appserver.CoordinationProjection, semanticEvent bool) {
				t.Helper()
				at := replayTestEpoch + second*1000
				live.clock = func() time.Time { return time.UnixMilli(at) }
				if semanticEvent {
					appServerTestNotify(t, live, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "turn", Projection: projection})
					records = append(records, replayTestRecord("event_msg", at, map[string]any{"type": "agent_coordination", "threadId": "main", "turnId": "turn", "projection": projection}))
				} else {
					item := map[string]any{"type": carrier, "id": "carrier", "call_id": "wait-call", "output": "opaque provider output, not parsed"}
					appServerTestNotify(t, live, "rawResponseItem/completed", map[string]any{"threadId": "main", "item": item, "coordination": projection})
					record := replayTestRecord("response_item", at, item)
					record["metadata"] = map[string]any{"coordination": projection}
					records = append(records, record)
				}
			}
			emit(0, requested, true)
			emit(1, facts, carrier == "event")
			reset := appserver.Message{Method: "thread/contextReset", Params: []byte(`{"threadId":"main","turnId":"reset-turn","generation":2,"requestId":"manual","resetId":"reset","status":"failed","diagnostic":"Reset unavailable"}`)}
			live.clock = func() time.Time { return time.UnixMilli(replayTestEpoch + 2000) }
			if err := live.message(reset); err != nil {
				t.Fatal(err)
			}
			records = append(records, replayTestRecord("event_msg", replayTestEpoch+2000, map[string]any{"type": "context_reset", "threadId": "main", "turnId": "reset-turn", "generation": 2, "requestId": "manual", "resetId": "reset", "status": "failed", "diagnostic": "Reset unavailable"}))
			emit(3, facts, carrier == "event")
			emit(4, requested, true)
			path := replayTestWrite(t, directory, "rollout-main.jsonl", records...)
			source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: filepath.Join(directory, "absent-history"), Seed: 17})
			if err != nil {
				t.Fatal(err)
			}
			if source.Items != 0 || len(source.Events) != 5 || len(source.Unsupported) != 0 {
				t.Fatalf("native receipts became simulated tools or disappeared: %+v", source)
			}
			p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
			t.Cleanup(p.close)
			if err := p.advance(source.End.Sub(source.Start)); err != nil {
				t.Fatal(err)
			}
			byID := func(source *sessionpresentation.ActivitySource) map[string]sessionpresentation.ActivityEntry {
				values := make(map[string]sessionpresentation.ActivityEntry)
				for _, record := range source.Entries {
					entry := record.Source
					if entry.Native != nil {
						values[entry.Native.Item] = entry
					}
				}
				return values
			}
			actual, expected := byID(p.ui.source), byID(live.attached.source)
			if len(actual) != 6 || len(expected) != len(actual) {
				t.Fatalf("replay duplicated or omitted native rows: actual=%v live=%v", actual, expected)
			}
			for id, want := range expected {
				got, ok := actual[id]
				if !ok || got.Kind != want.Kind || got.Text != want.Text || got.Agent != want.Agent || got.Native.Status != want.Native.Status || !reflect.DeepEqual(got.Native.Wait, want.Native.Wait) || !reflect.DeepEqual(got.Message, want.Message) {
					t.Fatalf("replay semantic row %s differs: got=%+v live=%+v", id, got, want)
				}
			}
			if actual["native:interrupt:interrupt"].Text != "Interrupted /root/worker" || actual["native:delivery:final"].Text != body || actual["native:delivery:final"].Native.Status != "completed" || actual["native:delivery:foreign"].Message.From != "/remote/observer" || actual["native:reset:reset"].Text != "Compaction not performed: Reset unavailable" {
				t.Fatalf("native authority/body/reset semantics changed: %+v", actual)
			}
			paint := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 44))
			if !strings.Contains(paint, "Interrupted /root/worker") || !strings.Contains(paint, "Reset unavailable") || strings.Contains(paint, "Earlier progress must not replay") {
				t.Fatalf("replay renderer lost terminal/reset semantics: %s", paint)
			}
			if wire := live.Host.Input.(*appServerTestInput); wire.Len() != 0 {
				t.Fatalf("native presentation dispatched host work: %s", wire.String())
			}
			if err := p.advance(0); err != nil {
				t.Fatal(err)
			}
			if entries := p.ui.source.Entries; len(entries) != 1 || entries[0].Source.Text != "Interrupt requested for /root/worker" {
				t.Fatal("backward replay retained a future terminal fact")
			}
		})
	}
}

func TestSessionUIReplayNativeMessageCarriersPreserveBodyAndAttribution(t *testing.T) {
	for _, carrier := range []string{"agent_message", "message"} {
		for _, kind := range []string{"NEW_TASK", "MESSAGE", "FINAL_ANSWER"} {
			for _, inherited := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/inherited=%t", carrier, kind, inherited), func(t *testing.T) {
					directory := t.TempDir()
					body := "Status: failed\nStatus: interrupted\nVerbatim native body."
					item := map[string]any{"type": carrier, "id": "native-message", "author": "/remote/observer", "recipient": "/root/worker", "content": []map[string]string{{"type": "input_text", "text": "Message Type: " + kind + "\nTask name: /root/worker\nSender: /remote/observer\nPayload:\n" + body}}}
					if carrier == "message" {
						provenance := "multi_agent.inter_agent_message"
						if kind == "FINAL_ANSWER" {
							provenance = "multi_agent.inter_agent_completion_message"
						}
						item["role"], item["internal_chat_message_metadata_passthrough"] = "assistant", map[string]any{"content_item_kinds": []string{provenance}}
					}
					meta := replayTestMeta("main")
					if !inherited {
						meta["payload"].(map[string]any)["agent_path"] = "/root/worker"
					}
					records := []map[string]any{meta}
					for _, at := range []int64{replayTestEpoch, replayTestEpoch + 1000} {
						record := replayTestRecord("response_item", at, item)
						record["metadata"] = map[string]any{"inherited_user_message": inherited}
						records = append(records, record)
					}
					path := replayTestWrite(t, directory, "rollout-main.jsonl", records...)
					source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: filepath.Join(directory, "absent-history")})
					if err != nil {
						t.Fatal(err)
					}
					p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
					t.Cleanup(p.close)
					if err := p.advance(source.End.Sub(source.Start)); err != nil {
						t.Fatal(err)
					}
					if source.Items != 0 || len(p.ui.childSource.Entries) != 1 || p.ui.Turn != "" {
						t.Fatal("turnless native message became model work or duplicate traffic")
					}
					if kind == "MESSAGE" && (len(p.ui.source.Entries) == 1) == inherited {
						t.Fatal("native recipient attribution leaked inherited child traffic into Main")
					}
					entry := p.ui.childSource.Entries[0].Source
					recipient := "Main"
					if inherited {
						recipient = "/root/worker"
					}
					if entry.Text != body || entry.Native.Status != "" || kind == "NEW_TASK" && (entry.Assignment == nil || entry.Assignment.From != "/remote/observer" || entry.Assignment.To != recipient) || kind != "NEW_TASK" && (entry.Message == nil || entry.Message.From != "/remote/observer" || entry.Message.To != recipient) {
						t.Fatalf("native payload inferred an outcome or changed attribution: %+v", entry)
					}
				})
			}
		}
	}
}
