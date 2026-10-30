package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAgentCompactionUsesSoleLedgerAndBothGates(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			t.Run(fmt.Sprintf("ackFirst=%t/success=%t", ackFirst, success), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					transport := &requestTransport{id: "compact"}
					u := Client[struct{}]{Thread: "main", Requests: NewRequests[struct{}](transport)}
					done := make(chan error, 1)
					go func() {
						_, err := u.Requests.Input(t.Context(), control.NativeInput{Thread: "main", ID: "checkpoint", Kind: "COMPACT", Run: true})
						done <- err
					}()
					u.Dispatch(<-u.Calls())
					if len(transport.sends) != 1 || transport.sends[0].method != "thread/compact/start" {
						t.Fatal(transport.sends)
					}
					ack := func() {
						if !u.CompleteCall(appserver.Message{ID: jsontext.Value(`compact`), Result: jsontext.Value(`{}`)}) {
							t.Fatal("ack not correlated")
						}
					}
					u.Started("compact-turn")
					if !u.AuxiliaryTurn("compact-turn") {
						t.Fatal("compaction became logical work")
					}
					if ackFirst {
						ack()
					} else {
						u.Completed(success)
					}
					synctest.Wait()
					select {
					case <-done:
						t.Fatal("single gate settled compact")
					default:
					}
					if u.Calls() != nil {
						t.Fatal("barrier released before both gates")
					}
					if ackFirst {
						u.Completed(success)
					} else {
						ack()
					}
					if err := <-done; (err == nil) != success {
						t.Fatalf("success=%t err=%v", success, err)
					}
					if u.Compaction.Pending() {
						t.Fatal("settled compaction still blocks")
					}
				})
			})
		}
	}
}

func TestClaudeCompactionRequiresNativeSuccessBoundaryAndTerminal(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "missing-boundary", "missing-result"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var runtime *ClaudeRuntime
				var compactInput claude.Input
				native := claudeRequestFunc(func(ctx context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
					if method != "input" {
						t.Fatalf("unexpected native method %s", method)
					}
					if err := json.Unmarshal(raw, &compactInput); err != nil {
						t.Fatal(err)
					}
					var message struct {
						Content string `json:"content"`
					}
					if err := json.Unmarshal(compactInput.Message, &message); err != nil {
						t.Fatal(err)
					}
					if message.Content != "/compact" || string(compactInput.Origin) != `{"kind":"coordinator"}` || !compactInput.IsSynthetic {
						t.Fatalf("native command modified: %s", raw)
					}
					event := claudeStarted(t, runtime, compactInput)
					if !event.Auxiliary {
						t.Fatal("compact start became logical assignment")
					}
					return jsontext.Value(`{}`), nil
				})
				runtime, _, _ = claudeFixture(t, native)
				_, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "retained", Kind: "MESSAGE", Message: "not for compact", Sender: "/root"})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "compact", Kind: "COMPACT", Run: true})
					done <- err
				}()
				synctest.Wait()
				emit := func(event map[string]any) ClaudeEvent {
					event["session_id"] = "native-session"
					raw, _ := json.Marshal(event)
					result, err := runtime.HandleEvent(t.Context(), raw)
					if err != nil {
						t.Fatal(err)
					}
					return result
				}
				if outcome != "missing-result" {
					value := "success"
					if outcome == "failed" {
						value = "failed"
					}
					emit(map[string]any{"type": "system", "subtype": "status", "compact_result": value, "compact_error": "fixture failure"})
				}
				if outcome != "missing-boundary" {
					emit(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "boundary", "compact_metadata": map[string]string{"trigger": "manual"}})
				}
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("native evidence skipped matching terminal")
				default:
				}
				result := emit(map[string]any{"type": "result", "subtype": "success", "uuid": "terminal", "user_message_uuid": compactInput.UUID, "terminal_reason": "completed"})
				if !result.Auxiliary || !result.Completed || len(result.Consumed) != 0 || result.Answer != "" {
					t.Fatalf("auxiliary result leaked work/input: %+v", result)
				}
				if err := <-done; (err == nil) != (outcome == "success") {
					t.Fatalf("outcome=%s err=%v", outcome, err)
				}
				if len(runtime.ledger.Retained) != 1 {
					t.Fatal("compaction consumed retained input")
				}
			})
		})
	}
}

func TestAgentCompactionCancellationPreservesLogicalSupersession(t *testing.T) {
	for _, started := range []bool{false, true} {
		u := Live{Thread: "main", Waiting: "checkpoint", LocalStop: "original"}
		u.Compaction.Begin(CompactIntent{})
		u.Compaction.call = &HostCall{result: make(chan hostCallResult, 1)}
		if started {
			u.Started("compact-turn")
		}
		intent := u.Interrupt()
		if intent.Wait != "checkpoint" || intent.Stop != "original" || started && intent.Turn != "compact-turn" || !started && !intent.Pending {
			t.Fatalf("lost exact cancellation: %+v", intent)
		}
	}
}
