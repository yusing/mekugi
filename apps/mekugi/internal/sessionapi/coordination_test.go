package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	history "github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

type coordinationHost struct {
	threadcontrol.Native
	threadcontrol.Runtime
	hub        *Hub
	mu         sync.Mutex
	inputs     []threadcontrol.NativeInput
	notices    chan threadcontrol.NativeInput
	failure    error
	resumes    int
	ask        func(*threadcontrol.Member, string) (string, error)
	afterInput func(threadcontrol.NativeInput)
}

func (h *coordinationHost) Peer(c threadcontrol.Identity, target string) (threadcontrol.Peer, error) {
	return h.hub.Peer(c, target)
}
func (h *coordinationHost) Resume(context.Context, string) (threadcontrol.Runtime, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resumes++
	return h, nil
}
func (h *coordinationHost) RuntimeFor(thread string) (threadcontrol.Runtime, error) {
	return h.hub.Requests(thread)
}
func (h *coordinationHost) Inspect(context.Context, string) (threadcontrol.SessionDetails, error) {
	return threadcontrol.SessionDetails{Model: new("observed-model")}, nil
}
func (h *coordinationHost) Confirm(context.Context, threadcontrol.NativeInput) error { return nil }
func (h *coordinationHost) Input(_ context.Context, input threadcontrol.NativeInput) (string, error) {
	h.mu.Lock()
	h.inputs = append(h.inputs, input)
	err := h.failure
	afterInput := h.afterInput
	h.mu.Unlock()
	h.notices <- input
	if afterInput != nil {
		afterInput(input)
	}
	if err != nil {
		return "", err
	}
	if input.Run {
		return input.Thread + "-wake", nil
	}
	return "", nil
}
func (h *coordinationHost) SideQuestion(_ context.Context, member *threadcontrol.Member, message string) (string, error) {
	return h.ask(member, message)
}
func (*coordinationHost) ObserveAdmitted(string, string, string, bool) bool { return true }

// Keep the two embedded interfaces' Runtime field distinct from Native.Runtime.
type coordinationNative struct{ *coordinationHost }

func (h coordinationNative) Runtime(thread string) (threadcontrol.Runtime, error) {
	return h.RuntimeFor(thread)
}

type coordinationFixture struct {
	service *threadcontrol.Service
	driver  *threadbinding.Driver
	host    *coordinationHost
	caller  threadcontrol.Identity
	cancel  context.CancelFunc
}

func coordinationPair(t *testing.T) (*Hub, *coordinationFixture, *coordinationFixture) {
	t.Helper()
	hub := NewHub(t.Context(), nil)
	makeOwner := func(id string) *coordinationFixture {
		retained, err := history.NewService(threadcontrol.RecordKinds())
		if err != nil {
			t.Fatal(err)
		}
		store, err := retained.Open(t.Context(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(session.Close)
		service := threadcontrol.New()
		member := &threadcontrol.Member{Thread: id, Session: id + "-auth", Task: "/root", Turn: id + "-turn", Generation: 1, Status: threadcontrol.Working, HostRunning: true, AudienceAdmitted: true, Limits: threadcontrol.Limits{MaxWaitMS: 1000}}
		if err := service.AdmitRoot(t.Context(), session, member); err != nil {
			t.Fatal(err)
		}
		driver, err := threadbinding.NewDriver(service)
		if err != nil {
			t.Fatal(err)
		}
		host := &coordinationHost{hub: hub, notices: make(chan threadcontrol.NativeInput, 20)}
		if err := driver.Connect(coordinationNative{host}, host); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		server := &Server{}
		server.BindRequests(host)
		server.BindCoordination(service, driver.DeliverMessages)
		hub.sessions[id] = &listedSession{server: server, runtime: &managedSession{ctx: ctx}}
		if err := driver.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})
		return &coordinationFixture{service, driver, host, threadcontrol.Identity{Thread: id, Session: id + "-auth", Turn: id + "-turn", Call: "call", Item: "exec"}, cancel}
	}
	return hub, makeOwner("source"), makeOwner("target")
}

func coordinationCall(t *testing.T, f *coordinationFixture, name, args string) jsontext.Value {
	t.Helper()
	value, err := f.driver.Call(t.Context(), f.caller, name, jsontext.Value(args))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCrossSessionMessageAttributionAndReceipt(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "uncertain"}[uncertain], func(t *testing.T) {
			_, source, target := coordinationPair(t)
			if err := target.service.HostCompleted(t.Context(), "target", "target-turn", threadcontrol.Completed, threadcontrol.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			if uncertain {
				target.host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost receipt")}
			}
			args := `{"target":"target","message":"payload"}`
			_, firstErr := source.driver.Call(t.Context(), source.caller, "send_message", jsontext.Value(args))
			if !uncertain && firstErr != nil {
				t.Fatal(firstErr)
			}
			target.host.mu.Lock()
			target.host.failure = nil
			target.host.mu.Unlock()
			value := coordinationCall(t, source, "send_message", args)
			var receipt struct {
				Session string `json:"session_id"`
				State   string `json:"state"`
			}
			if err := json.Unmarshal(value, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Session != "target" || receipt.State != "confirmed" {
				t.Fatalf("receipt: %s", value)
			}
			target.host.mu.Lock()
			defer target.host.mu.Unlock()
			if len(target.host.inputs) != 1 || target.host.resumes != 0 {
				t.Fatalf("inputs=%d resumes=%d", len(target.host.inputs), target.host.resumes)
			}
			input := target.host.inputs[0]
			if input.Sender != "agent:main (session source, thread source)" || input.Message != "payload" || input.Run {
				t.Fatalf("input: %+v", input)
			}
			member, _ := target.service.Member("target")
			if member.Status != threadcontrol.Completed || member.HostRunning {
				t.Fatalf("idle recipient changed: %+v", member)
			}
		})
	}
}

func TestCrossSessionAuthenticationAndReadOnlyOperations(t *testing.T) {
	hub, source, target := coordinationPair(t)
	for _, caller := range []threadcontrol.Identity{{Thread: "source", Session: "forged", Turn: "source-turn"}, {Thread: "absent", Session: "source-auth"}} {
		if _, err := hub.Peer(caller, "target"); err == nil {
			t.Fatal("unauthenticated source accepted")
		}
	}
	for _, name := range []string{"followup_task", "interrupt_agent"} {
		args := `{"target":"target"}`
		if name == "followup_task" {
			args = `{"target":"target","message":"forbidden"}`
		}
		if _, err := source.driver.Call(t.Context(), source.caller, name, jsontext.Value(args)); err == nil {
			t.Fatalf("foreign %s accepted", name)
		}
	}
	inspection := coordinationCall(t, source, "inspect", `{"target":"target"}`)
	var details struct {
		Session string `json:"session_id"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(inspection, &details); err != nil {
		t.Fatal(err)
	}
	if details.Session != "target" || details.Model != "observed-model" {
		t.Fatalf("inspect: %s", inspection)
	}
	for _, prefix := range []string{"agent:main", "agent:main/absent"} {
		value := coordinationCall(t, source, "list_agents", `{"session_id":"target","path_prefix":"`+prefix+`"}`)
		var roster struct {
			Agents []struct {
				Thread string `json:"thread_id"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(value, &roster); err != nil {
			t.Fatal(err)
		}
		if prefix == "agent:main" && (len(roster.Agents) != 1 || roster.Agents[0].Thread != "target") || prefix != "agent:main" && len(roster.Agents) != 0 {
			t.Fatalf("roster: %s", value)
		}
	}
	target.cancel()
	for _, name := range []string{"inspect", "send_message", "wait_agent"} {
		args := `{"target":"target"}`
		if name == "send_message" {
			args = `{"target":"target","message":"no resume"}`
		}
		if name == "wait_agent" {
			args = `{"target":["target"]}`
		}
		if _, err := source.driver.Call(t.Context(), source.caller, name, jsontext.Value(args)); err == nil {
			t.Fatalf("inactive %s accepted", name)
		}
	}
}

func TestCrossSessionSideQuestion(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "answer", true: "stale"}[stale], func(t *testing.T) {
			_, source, target := coordinationPair(t)
			target.host.ask = func(member *threadcontrol.Member, question string) (string, error) {
				if member.Thread != "target" || !strings.HasPrefix(question, "agent:main (session source, thread source)\n") {
					t.Fatalf("unattributed question %q", question)
				}
				if stale {
					target.cancel()
				}
				return "answer", nil
			}
			value, err := source.driver.Call(t.Context(), source.caller, "send_message", jsontext.Value(`{"target":"target","message":"question","btw":true}`))
			if stale {
				if err == nil {
					t.Fatal("stale answer accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Answer  string `json:"answer"`
				Session string `json:"session_id"`
			}
			if err := json.Unmarshal(value, &response); err != nil {
				t.Fatal(err)
			}
			if response.Answer != "answer" || response.Session != "target" || len(target.host.inputs) != 0 {
				t.Fatalf("response: %s", value)
			}
		})
	}
}

func TestCrossSessionWaitObservesPublicationAndRuntimeExit(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "ended"}[ended], func(t *testing.T) {
			_, source, target := coordinationPair(t)
			coordinationCall(t, source, "wait_agent", `{"target":["target"]}`)
			if err := source.service.Execution(t.Context(), "source", "source-turn", "exec", "", true); err != nil {
				t.Fatal(err)
			}
			if stop, err := source.service.ClaimLocalStop(t.Context(), "source", "source-turn", "exec"); err != nil || stop == nil {
				t.Fatalf("stop: %v %v", stop, err)
			}
			if err := source.service.HostCompleted(t.Context(), "source", "source-turn", threadcontrol.Completed, threadcontrol.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			if ended {
				target.cancel()
			} else if err := target.service.HostCompleted(t.Context(), "target", "target-turn", threadcontrol.Completed, threadcontrol.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			select {
			case input := <-source.host.notices:
				if input.Wake == nil || input.Wake.Event == nil || input.Wake.Event.Session != "target" || input.Wake.Event.Source != "target" {
					t.Fatalf("wake: %+v", input)
				}
				if ended && input.Wake.Event.Kind != "unavailable" {
					t.Fatalf("ended event: %+v", input.Wake.Event)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("foreign observation failed to wake caller")
			}
			state, err := source.service.Snapshot("source")
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Members) != 1 {
				t.Fatal("foreign member merged into local tree")
			}
		})
	}
}

func TestCrossSessionSideQuestionRejectsNewGeneration(t *testing.T) {
	_, source, target := coordinationPair(t)
	target.host.ask = func(_ *threadcontrol.Member, _ string) (string, error) {
		if err := target.service.HostCompleted(t.Context(), "target", target.caller.Turn, threadcontrol.Completed, threadcontrol.HostFailure{}); err != nil {
			t.Fatal(err)
		}
		next := target.caller
		next.Turn = "replacement-turn"
		if err := target.service.RequestStarted(t.Context(), next, true); err != nil {
			t.Fatal(err)
		}
		return "obsolete answer", nil
	}
	_, err := source.driver.Call(t.Context(), source.caller, "send_message", jsontext.Value(`{"target":"target","message":"question","btw":true}`))
	if err == nil {
		t.Fatal("answer from previous target generation accepted")
	}
}

func TestCrossSessionWaitReexecutionAfterTargetExit(t *testing.T) {
	_, source, target := coordinationPair(t)
	if _, err := source.driver.Call(t.Context(), source.caller, "wait_agent", jsontext.Value(`{"target":["target"],"timeout_ms":1001}`)); err == nil {
		t.Fatal("invalid foreign wait timeout accepted")
	}
	if member, _ := source.service.Member("source"); member.Wait != nil {
		t.Fatal("invalid registration changed prior state")
	}
	args := `{"target":["target"]}`
	first := coordinationCall(t, source, "wait_agent", args)
	target.cancel()
	second := coordinationCall(t, source, "wait_agent", args)
	var original, repeated threadcontrol.WaitResult
	if err := json.Unmarshal(first, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated.ID != original.ID || len(repeated.Targets) != 1 || repeated.Targets[0] != "target" {
		t.Fatalf("repeated wait lost selection: %s", second)
	}
}

func TestCrossSessionMessageDoesNotResumeEndedWaiter(t *testing.T) {
	_, source, target := coordinationPair(t)
	coordinationCall(t, target, "wait_agent", `{"target":["source"]}`)
	if err := target.service.Execution(t.Context(), "target", "target-turn", "exec", "", true); err != nil {
		t.Fatal(err)
	}
	if stop, err := target.service.ClaimLocalStop(t.Context(), "target", "target-turn", "exec"); err != nil || stop == nil {
		t.Fatalf("stop: %v %v", stop, err)
	}
	if err := target.service.HostCompleted(t.Context(), "target", "target-turn", threadcontrol.Completed, threadcontrol.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	target.host.afterInput = func(input threadcontrol.NativeInput) {
		if !input.Run {
			target.cancel()
		}
	}
	_, _ = source.driver.Call(t.Context(), source.caller, "send_message", jsontext.Value(`{"target":"target","message":"progress"}`))
	target.host.mu.Lock()
	defer target.host.mu.Unlock()
	if target.host.resumes != 0 || len(target.host.inputs) != 1 || target.host.inputs[0].Run {
		t.Fatalf("ended receiver resumed: resumes=%d inputs=%+v", target.host.resumes, target.host.inputs)
	}
}
