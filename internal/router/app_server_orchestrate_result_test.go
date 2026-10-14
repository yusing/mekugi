package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateDiagnosisOverlappingAnswers(t *testing.T) {
	for _, delayedAck := range []bool{false, true} {
		t.Run(fmt.Sprintf("delayed_ack=%t", delayedAck), func(t *testing.T) {
			testDiagnosisOverlappingAnswers(t, delayedAck)
		})
	}
}

func testDiagnosisOverlappingAnswers(t *testing.T, delayedAck bool) {
	u, launch := orchestratePendingWorkflow(t, nil, orchestrateVCSWorkspace(t, "git"), nil, &orchestrateSpawnInput{TaskName: "batch", Kind: orchestrate.WorkflowDiagnosis, Message: "diagnose"})
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"first"}}`)
	wire := u.client.Input.(*appServerTestInput)
	takeAnswer := func(turn string) btwTestRPC {
		t.Helper()
		request, params := workflowTake(t, u, wire, "thread/inject_items", "main", "")
		text := params["items"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
		if text != "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/batch\nPayload:\n"+turn {
			t.Fatal("a later completion replaced an undelivered answer", text)
		}
		return request
	}
	var first btwTestRPC
	for _, turn := range []string{"first", "second"} {
		appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"child","turnId":%q,"item":{"type":"agentMessage","text":%q}}}`, turn, turn))
		appServerTestMessage(t, u, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"child","turn":{"id":%q,"status":"completed"}}}`, turn))
		if delayedAck {
			if turn == "first" {
				drainOrchestrateWork(t, u)
				first = takeAnswer(turn)
			} else {
				// Queue A's acknowledgement behind B's completion, before callbacks run.
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, first.ID))
			}
		}
	}
	drainOrchestrateWork(t, u)
	if !delayedAck {
		first = takeAnswer("first")
	}
	orchestrateTestReply(t, u, takeAnswer("second"), `{}`)
	order := []string{"first", "second"}
	if !delayedAck {
		// Replies can settle in reverse order without acknowledging the wrong turn.
		orchestrateTestReply(t, u, first, `{}`)
		order = []string{"second", "first"}
	}
	for _, turn := range order {
		wait := &orchestrateCommand{ctx: t.Context(), main: "main", workspace: u.session.cwd, tool: "wait_agent", callID: turn, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(wait)
		drainOrchestrateWork(t, u)
		if event := waitTestEvent(t, <-wait.reply); event.Kind != "done" || event.Turn != turn || event.Message != "" {
			t.Fatal("acknowledgement did not settle its diagnosis turn", event)
		}
	}
	if wire.Len() != 0 {
		t.Fatal("overlapping storage work repeated delivery or started a turn", wire.String())
	}
	u, wire = resumeOrchestrationMain(t, u.proxy.orchestration.store, u.session.cwd)
	drainOrchestrateWork(t, u)
	for _, turn := range []string{"first", "second"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"child","turn":{"id":%q,"status":"completed"}}}`, turn))
	}
	if strings.Contains(wire.String(), "thread/inject_items") || strings.Contains(wire.String(), "turn/start") {
		t.Fatal("restart repeated a superseded diagnosis answer", wire.String())
	}
}

func TestAppServerOrchestrateDiagnosisAnswerRestart(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "acknowledged"}[acknowledge], func(t *testing.T) {
			old, launch := orchestratePendingWorkflow(t, nil, orchestrateVCSWorkspace(t, "git"), nil, &orchestrateSpawnInput{TaskName: "batch", Kind: orchestrate.WorkflowDiagnosis, Message: "diagnose"})
			orchestrateTestReply(t, old, launch, `{"turn":{"id":"diagnosis"}}`)
			answer := " Exact diagnosis.\nEvidence: café\n"
			orchestrateTestMessage(t, old, string(mustMarshalJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "child", "turnId": "diagnosis", "item": map[string]any{"type": "agentMessage", "text": answer}}})))
			completion := `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"diagnosis","status":"completed"}}}`
			appServerTestMessage(t, old, completion)
			// Crash after the completion write, before its UI callback can dispatch.
			// Independent journal lifecycle results share this UI channel.
			store, workspace := old.proxy.orchestration.store, old.session.cwd
			for {
				select {
				case complete := <-old.orchestrateCompletions:
					batches, err := store.Snapshot(workspace, "main")
					if err != nil {
						t.Fatal(err)
					}
					if batches[0].Launch.HostStatus != "completed" {
						old.completeOrchestrateWork(complete)
						continue
					}
					if batches[0].Workflow.Outcome == nil || batches[0].Workflow.Outcome.Message != answer || batches[0].Workflow.Outcome.State != orchestrate.DispatchQueued {
						t.Fatal("completion did not retain its exact unsent answer", batches)
					}
					old.orchestrateJobs = nil
				case <-time.After(5 * time.Second):
					t.Fatal("completion persistence timeout")
				}
				break
			}
			if old.client.Input.(*appServerTestInput).Len() != 0 {
				t.Fatal("answer dispatched before crash boundary")
			}
			// Fresh Main, with only its retained run. No child history or live parent.
			u, wire := resumeOrchestrationMain(t, store, workspace)
			drainOrchestrateWork(t, u)
			inject, params := workflowTake(t, u, wire, "thread/inject_items", "main", "")
			var items []struct {
				Role    string
				Content []struct{ Text string }
			}
			if err := json.Unmarshal(mustMarshalJSON(params["items"]), &items, json.MatchCaseInsensitiveNames(true)); err != nil || len(items) != 1 || items[0].Role != "assistant" || len(items[0].Content) != 1 || items[0].Content[0].Text != "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/batch\nPayload:\n"+answer {
				t.Fatal("recovered answer changed attribution or bytes", params, err)
			}
			if acknowledge {
				orchestrateTestReply(t, u, inject, `{}`)
				wait := &orchestrateCommand{ctx: t.Context(), main: "main", workspace: workspace, tool: "wait_agent", callID: "completion", reply: make(chan orchestrateResult, 1)}
				u.startOrchestratedChild(wait)
				drainOrchestrateWork(t, u)
				if event := waitTestEvent(t, <-wait.reply); event.Turn != "diagnosis" || event.Kind != "done" || event.Message != "" {
					t.Fatal("completion wait changed or repeated answer", event)
				}
			}
			batches, err := store.Snapshot(workspace, "main")
			want := orchestrate.DispatchIntent
			if acknowledge {
				want = orchestrate.DispatchAcknowledged
			}
			if err != nil || batches[0].Workflow.Outcome.State != want {
				t.Fatal("host acknowledgement was not retained", batches, err)
			}
			orchestrateTestMessage(t, u, completion)
			if strings.Contains(wire.String(), "turn/start") || strings.Contains(wire.String(), "thread/inject_items") {
				t.Fatal("recovery or duplicate completion replayed an effect", wire.String())
			}
			u, wire = resumeOrchestrationMain(t, store, workspace)
			drainOrchestrateWork(t, u)
			orchestrateTestMessage(t, u, completion)
			if strings.Contains(wire.String(), "turn/start") || strings.Contains(wire.String(), "thread/inject_items") {
				t.Fatal("restart repeated an acknowledged or uncertain answer", wire.String())
			}
		})
	}
}
