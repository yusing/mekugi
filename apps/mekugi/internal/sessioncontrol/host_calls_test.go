package sessioncontrol

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestHostCallSharesLedgerAndSettlesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "host-id"}
		requests := NewRequests[string](transport)
		result := make(chan hostCallResult, 1)
		go func() {
			value, err := requests.call(t.Context(), "child", "thread/start", jsontext.Value(`{"cwd":"/child"}`), nil)
			result <- hostCallResult{value, err}
		}()
		call := <-requests.Calls()
		if requests.Pending() || len(transport.sends) != 0 {
			t.Fatal("handler dispatched outside the event loop")
		}
		requests.Dispatch(call)
		if pending, ok := requests.Lookup("host-id"); !ok || pending.Scope != "child" || pending.Method != "thread/start" {
			t.Fatalf("operation missing from existing ledger: %+v", pending)
		}
		message := appserver.Message{ID: jsontext.Value(`host-id`), Result: jsontext.Value(`{"thread":{"id":"child"}}`)}
		if !requests.CompleteCall(message) || requests.CompleteCall(message) || requests.Pending() {
			t.Fatal("reply did not settle exactly once")
		}
		got := <-result
		if got.err != nil || string(got.value) != string(message.Result) || len(transport.sends) != 1 {
			t.Fatalf("result=%s err=%v sends=%d", got.value, got.err, len(transport.sends))
		}
	})
}

func TestHostCallCancellationAndUncertainDispatch(t *testing.T) {
	for _, phase := range []string{"before_handoff", "before_dispatch", "after_dispatch", "retired", "closed", "rejected", "send_failure"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &requestTransport{id: "request"}
				requests := NewRequests[struct{}](transport)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if phase == "before_handoff" {
					cancel()
				}
				result := make(chan error, 1)
				go func() {
					_, err := requests.call(ctx, "work", "turn/start", jsontext.Value(`{"input":[]}`), nil)
					result <- err
				}()
				if phase != "before_handoff" {
					call := <-requests.Calls()
					if phase == "before_dispatch" {
						cancel()
					}
					if phase == "send_failure" {
						transport.err = errors.New("partial write")
					}
					requests.Dispatch(call)
					switch phase {
					case "after_dispatch":
						cancel()
					case "retired":
						requests.Retire("work")
					case "closed":
						requests.Close()
					case "rejected":
						requests.CompleteCall(appserver.Message{ID: jsontext.Value(`request`), Error: &appserver.Error{Code: -32600, Message: "thread not found"}})
					}
				}
				err := <-result
				failure, ok := errors.AsType[*binding.DispatchError](err)
				want := binding.Uncertain
				if phase == "before_handoff" || phase == "before_dispatch" {
					want = binding.NotDispatched
					if len(transport.sends) != 0 {
						t.Fatal("canceled operation executed")
					}
				} else if phase == "rejected" {
					want = binding.HostRejected
				}
				if !ok || failure.Outcome != want {
					t.Fatalf("outcome=%v want %s", err, want)
				}
				if phase == "after_dispatch" {
					if !requests.Pending() || !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`request`), Result: jsontext.Value(`{}`)}) {
						t.Fatal("cancellation discarded the dispatched operation's ledger identity")
					}
				}
			})
		})
	}
}

func TestClientFailedDispatchKeepsInputAdmission(t *testing.T) {
	for _, method := range []string{"turn/start", "thread/compact/start"} {
		t.Run(method, func(t *testing.T) {
			transport := &requestTransport{err: errors.New("partial write")}
			client := &Client[struct{}]{Requests: NewRequests[struct{}](transport)}
			client.Thread = "main"
			call := &HostCall{ctx: t.Context(), scope: "main", method: method, result: make(chan hostCallResult, 1)}
			client.Dispatch(call)
			failure, ok := errors.AsType[*binding.DispatchError]((<-call.result).err)
			if !ok || failure.Outcome != binding.Uncertain || call.phase.Load() != callDispatched {
				t.Fatalf("failed send lost uncertainty: %v", failure)
			}
			if client.AwaitingTurn || client.Starting() || client.Compaction.Pending() || client.Requests.Pending() {
				t.Fatal("failed send blocked new input")
			}
		})
	}
}

func TestNativeInputUsesAttributedHostOperations(t *testing.T) {
	for _, run := range []bool{false, true} {
		t.Run(fmt.Sprint(run), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &requestTransport{id: "native"}
				requests := NewRequests[struct{}](transport)
				input := control.NativeInput{Thread: "child", ParentTurn: "parent-turn", ID: "message-1", Sender: "/root", Recipient: "/root/task", Kind: "FINAL_ANSWER", Message: "payload", Run: run}
				result := make(chan error, 1)
				go func() {
					turn, err := requests.Input(t.Context(), input)
					if err == nil && (run && turn != "turn" || !run && turn != "") {
						err = fmt.Errorf("unexpected turn %q", turn)
					}
					result <- err
				}()
				requests.Dispatch(<-requests.Calls())
				var params struct {
					Thread string `json:"threadId"`
					Input  []any  `json:"input"`
					Items  []struct {
						ID, Role, Phase string
						Content         []struct{ Type, Text string }
						Metadata        struct {
							Kinds []string `json:"content_item_kinds"`
						} `json:"internal_chat_message_metadata_passthrough"`
					} `json:"items"`
					Output struct {
						Namespace, Name, Output string
						Retain                  bool
					} `json:"toolOutput"`
				}
				raw := transport.sends[0].params.(jsontext.Value)
				if err := json.Unmarshal(raw, &params, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				text := "Message Type: FINAL_ANSWER\nTask name: /root/task\nSender: /root\nPayload:\npayload"
				if params.Thread != "child" {
					t.Fatal("recipient identity lost")
				}
				if run {
					text = "Operation ID: message-1\n" + text
					if transport.sends[0].method != "turn/start" || len(params.Input) != 0 || len(params.Items) != 0 || params.Output.Namespace != "threadcontrol" || params.Output.Name != "followup_task" || params.Output.Output != text || !params.Output.Retain {
						t.Fatalf("native run acquired incorrect authority: %s", raw)
					}
				} else {
					if transport.sends[0].method != "thread/inject_items" || len(params.Items) != 1 {
						t.Fatalf("message started work: %s", raw)
					}
					item := params.Items[0]
					id := fmt.Sprintf("threadcontrol_%x", sha256.Sum256([]byte(input.ID)))
					if item.ID != id || item.Role != "assistant" || item.Phase != "commentary" || len(item.Content) != 1 || item.Content[0].Type != "output_text" || item.Content[0].Text != text || !slices.Equal(item.Metadata.Kinds, []string{"multi_agent.inter_agent_completion_message"}) {
						t.Fatalf("attribution lost: %s", raw)
					}
				}
				request, _ := requests.Lookup("native")
				answer := request.NativeDelivery()
				if (answer != nil) != !run || answer != nil && (answer.ID != input.ID || answer.Sender != input.Sender || answer.Message != input.Message) {
					t.Fatalf("native answer attribution: %+v", answer)
				}
				requests.CompleteCall(appserver.Message{ID: jsontext.Value(`native`), Result: jsontext.Value(`{"turn":{"id":"turn"}}`)})
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestPreparedNativeAdmissionRetainsCanonicalAuthority(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "child.jsonl")
	settings := control.Settings{Model: "actual", Provider: "provider", Workspace: workspace, Summary: new("auto"), Tier: new("default"), PermissionProfile: jsontext.Value(`{"type":"disabled"}`), ApprovalPolicy: jsontext.Value(`"never"`)}
	prepared := &PreparedThread{Parent: "parent", Settings: settings}
	transport := &requestTransport{id: "start"}
	requests := NewRequests[struct{}](transport)
	if _, err := requests.OpenSession(&Startup{Prepared: prepared}, &Live{}, false, struct{}{}); err != nil {
		t.Fatal(err)
	}
	params := transport.sends[0].params.(map[string]any)
	config := params["config"].(map[string]jsontext.Value)
	if transport.sends[0].method != "thread/start" || params["sandbox"] != "danger-full-access" || params["cwd"] != workspace || params["serviceTier"] != "default" || string(config["model_reasoning_summary"]) != `"auto"` {
		t.Fatalf("prepared startup changed authority: %+v", transport.sends[0])
	}
	for _, inherited := range []bool{false, true} {
		owner := "child"
		if inherited {
			owner = "parent"
		}
		admission := binding.AdmissionSettings{Model: settings.Model, Provider: settings.Provider, Workspace: workspace, Summary: settings.Summary, Tier: settings.Tier, PermissionProfile: settings.PermissionProfile, ApprovalPolicy: settings.ApprovalPolicy}
		record, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "thread_settings_applied", "thread_id": owner, "thread_settings": admission}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"+string(record)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		receipt, err := prepared.Confirm(control.ThreadInfo{Thread: "child", Session: "session", Path: path})
		if err != nil || receipt.Uncertain || receipt.Thread != "child" || receipt.Session != "session" || receipt.Path != path {
			t.Fatalf("inherited=%t receipt=%+v err=%v", inherited, receipt, err)
		}
		if err := confirmNativeSettings("child", path, settings); (err != nil) != inherited {
			t.Fatalf("inherited=%t authority: %v", inherited, err)
		}
		retained := &PreparedThread{Parent: "parent", Settings: settings, Resume: &control.ThreadInfo{Thread: "child", Session: "session", Path: path}}
		startup := Startup{Prepared: retained, Config: map[string]any{"model": "changed-launch-default"}}
		resuming := &requestTransport{id: "resume"}
		requests := NewRequests[struct{}](resuming)
		opening, err := requests.OpenSession(&startup, &Live{}, false, struct{}{})
		if (err != nil) != inherited || opening != OpeningResume {
			t.Fatalf("retained startup: opening=%v, err=%v", opening, err)
		}
		if inherited {
			if len(resuming.sends) != 0 {
				t.Fatal("unconfirmed identity acquired a runtime")
			}
			continue
		}
		params := resuming.sends[0].params.(map[string]any)
		if len(resuming.sends) != 1 || resuming.sends[0].method != "thread/resume" || params["threadId"] != "child" || params["model"] != settings.Model || params["cwd"] != workspace || startup.Thread != "child" || requests.preparation.Load() != retained {
			t.Fatalf("resume did not retain identity and frozen authority: %+v", resuming.sends)
		}
		if _, err := retained.Confirm(*retained.Resume); err != nil {
			t.Fatal(err)
		}
		foreign := *retained.Resume
		foreign.Session = "other-session"
		if _, err := retained.Confirm(foreign); err == nil {
			t.Fatal("resume accepted a different retained session")
		}
	}
}

type tierPreparationHost struct {
	control.Native
	threadbinding.ChildAudience
	owner control.Runtime
}

type tierPreparationRuntime struct {
	control.Runtime
	info   control.ThreadInfo
	config jsontext.Value
}

func (h tierPreparationHost) Runtime(string) (control.Runtime, error) { return h.owner, nil }
func (h tierPreparationRuntime) Thread(context.Context, string) (control.ThreadInfo, error) {
	return h.info, nil
}
func (h tierPreparationRuntime) Configuration(context.Context, string, string) (jsontext.Value, error) {
	return h.config, nil
}

func TestPreparedRoleTierNativeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, role, features string
		inherited, want      *string
	}{
		{"disabled priority", "priority", `{"fast_mode":false}`, nil, nil},
		{"legacy fast", "fast", `{}`, nil, new("priority")},
		{"disabled legacy fast", "fast", `{"fast_mode":false}`, nil, nil},
		{"enabled priority", "priority", `{}`, nil, new("priority")},
		{"disabled ultrafast", "ultrafast", `{"ultrafast_mode":false}`, nil, nil},
		{"independent ultrafast", "ultrafast", `{"fast_mode":false}`, nil, new("ultrafast")},
		{"independent priority", "priority", `{"ultrafast_mode":false}`, nil, new("priority")},
		{"independent flex", "flex", `{"fast_mode":false,"ultrafast_mode":false}`, nil, new("flex")},
		{"enabled default", "default", `{}`, nil, new("default")},
		{"disabled default", "default", `{"fast_mode":false}`, nil, nil},
		{"unsupported role tier", "unlisted", `{}`, nil, nil},
		{"session wins", "priority", `{}`, new("flex"), new("flex")},
		{"cleared session does not fall back", "flex", `{"fast_mode":false}`, new("priority"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			rolePath := filepath.Join(workspace, "worker.toml")
			if err := os.WriteFile(rolePath, fmt.Appendf(nil, "model = 'selected'\nservice_tier = %q\n", tc.role), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workspace, "parent.jsonl")
			parent := binding.AdmissionSettings{Model: "parent", Provider: "provider", Workspace: workspace, Tier: tc.inherited, PermissionProfile: jsontext.Value(`{"type":"disabled"}`), ApprovalPolicy: jsontext.Value(`"never"`)}
			writeSettings := func(thread string, settings binding.AdmissionSettings) {
				t.Helper()
				record, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "thread_settings_applied", "thread_id": thread, "thread_settings": settings}})
				if err != nil {
					t.Fatal(err)
				}
				data := fmt.Appendf(nil, "{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n%s\n", thread, record)
				data = fmt.Appendf(data, "{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"turn\",\"model\":%q,\"cwd\":%q,\"permission_profile\":{\"type\":\"disabled\"},\"approval_policy\":\"never\"}}\n", settings.Model, workspace)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeSettings("parent", parent)
			retained, err := history.NewService(control.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := retained.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "parent")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			driver, err := threadbinding.NewDriver(control.New())
			if err != nil {
				t.Fatal(err)
			}
			host := tierPreparationHost{owner: tierPreparationRuntime{info: control.ThreadInfo{Thread: "parent", Session: "session", Path: path}, config: fmt.Appendf(nil, `{"layers":[{"name":{},"config":{"agents":{"worker":{"config_file":%q}}}}],"config":{"features":%s}}`, rolePath, tc.features)}}
			if err := driver.Connect(host, host); err != nil {
				t.Fatal(err)
			}
			caller := control.Identity{Thread: "parent", Session: "session", Turn: "turn", Call: "spawn", Item: "exec"}
			if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
				t.Fatal(err)
			}
			child, err := driver.PrepareSpawn(t.Context(), binding.MCPIdentity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item, Model: "parent"}, threadbinding.SpawnOptions{TaskName: "worker", Message: "assignment", AgentType: new("worker"), ForkTurns: new("none")}, []threadbinding.Model{{Name: "selected", ServiceTiers: []string{"priority", "ultrafast"}}})
			if err != nil {
				t.Fatal(err)
			}
			transport := &requestTransport{id: "start"}
			requests := NewRequests[struct{}](transport)
			prepared := &PreparedThread{Parent: "parent", Settings: child.Settings}
			if _, err := requests.OpenSession(&Startup{Prepared: prepared}, &Live{}, false, struct{}{}); err != nil {
				t.Fatal(err)
			}
			params := transport.sends[0].params.(map[string]any)
			if tc.want == nil && params["serviceTier"] != nil || tc.want != nil && params["serviceTier"] != *tc.want {
				t.Fatalf("native startup tier=%v, want %v", params["serviceTier"], tc.want)
			}
			// Native configuration/session gating produces this canonical tier.
			// Compare it through the admission consumer, not a relaxed tier match.
			canonical := parent
			canonical.Model, canonical.Tier = "selected", tc.want
			writeSettings("child", canonical)
			if err := confirmNativeSettings("child", path, child.Settings); err != nil {
				t.Fatalf("prepared role tier conflicts with native authority: %v", err)
			}
			if !reflect.DeepEqual(child.Settings.Tier, tc.want) {
				t.Fatalf("tier=%v, want %v", child.Settings.Tier, tc.want)
			}
			canonical.Tier = new("different")
			writeSettings("child", canonical)
			if err := confirmNativeSettings("child", path, child.Settings); err == nil {
				t.Fatal("tier mismatch bypassed strict authority comparison")
			}
		})
	}
}

func TestPreparedNativeFollowupCannotBypassRejectedAuthority(t *testing.T) {
	for _, field := range []string{"model", "summary", "tier"} {
		t.Run(field, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "child.jsonl")
				settings := control.Settings{Model: "expected", Provider: "provider", Workspace: filepath.Dir(path), Summary: new("auto"), Tier: new("default"), PermissionProfile: jsontext.Value(`{"type":"disabled"}`), ApprovalPolicy: jsontext.Value(`"never"`)}
				wrong := binding.AdmissionSettings{Model: settings.Model, Provider: settings.Provider, Workspace: settings.Workspace, Summary: settings.Summary, Tier: settings.Tier, PermissionProfile: settings.PermissionProfile, ApprovalPolicy: settings.ApprovalPolicy}
				switch field {
				case "model":
					wrong.Model = "other"
				case "summary":
					wrong.Summary = new("detailed")
				case "tier":
					wrong.Tier = new("flex")
				}
				record, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "thread_settings_applied", "thread_id": "child", "thread_settings": wrong}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"+string(record)+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				transport := &requestTransport{id: "native-read"}
				requests := NewRequests[struct{}](transport)
				requests.preparation.Store(&PreparedThread{Parent: "parent", Settings: settings})
				thread, _ := json.Marshal(map[string]any{"thread": control.ThreadInfo{Thread: "child", Session: "session", Path: path}})
				for _, input := range []control.NativeInput{{Thread: "child", Run: true, Settings: &settings}, {Thread: "child", Run: true, Kind: "NEW_TASK", Message: "followup"}, {Thread: "child", Kind: "HUMAN_INPUT"}} {
					done := make(chan error, 1)
					go func() {
						if input.Kind == "HUMAN_INPUT" {
							done <- requests.ConfirmPreparedInput(t.Context(), input.Thread)
						} else {
							_, err := requests.Input(t.Context(), input)
							done <- err
						}
					}()
					requests.Dispatch(<-requests.Calls())
					requests.CompleteCall(appserver.Message{ID: jsontext.Value(`native-read`), Result: thread})
					failure, ok := errors.AsType[*binding.DispatchError](<-done)
					if !ok || failure.Outcome != binding.NotDispatched || requests.preparation.Load() == nil {
						t.Fatalf("unconfirmed execution: failure=%v preparation=%v", failure, requests.preparation.Load())
					}
				}
				for _, send := range transport.sends {
					if send.method != "thread/read" {
						t.Fatalf("execution escaped rejected admission: %s", send.method)
					}
				}
			})
		})
	}
}
