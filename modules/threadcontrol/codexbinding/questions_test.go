package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type questionWithoutRuntime struct{ control.Native }

func (questionWithoutRuntime) Runtime(string) (control.Runtime, error) {
	return nil, errors.New("no live runtime")
}

func TestSideQuestionKeepsLogicalStateAndOrdinaryDelivery(t *testing.T) {
	for _, state := range []string{"running", "waiting", "completed", "paused"} {
		t.Run(state, func(t *testing.T) {
			driver, host, _, caller := reconciliationFixture(t)
			target := "child"
			switch state {
			case "waiting":
				if _, err := driver.control.RegisterWait(t.Context(), caller, []string{"child"}, false, control.WaitAll, nil, time.Now()); err != nil {
					t.Fatal(err)
				}
				target = "root"
				caller = control.Identity{Thread: "child", Session: "child-session", Turn: "child-turn", Call: "question", Item: "exec"}
			case "completed":
				if err := driver.control.HostCompleted(t.Context(), target, "child-turn", control.Completed, control.HostFailure{}); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if err := driver.control.Pause(t.Context(), target); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := driver.control.Member(target)
			inputs, confirmations, resumes := len(host.inputs), len(host.confirmed), host.resumes
			calls := 0
			host.ask = func(_ context.Context, member *control.Member, text string) (string, error) {
				calls++
				if member.Thread != target || member.Generation != before.Generation || text != "question" {
					t.Fatal("lost authenticated question identity")
				}
				return "answer", nil
			}
			args, _ := json.Marshal(map[string]any{"target": target, "message": "question", "btw": true})
			result, err := driver.Call(t.Context(), caller, "send_message", args)
			if err != nil {
				t.Fatal(err)
			}
			var answer struct {
				Answer string `json:"answer"`
				Thread string `json:"thread_id"`
			}
			if err := json.Unmarshal(result, &answer); err != nil || answer.Answer != "answer" || answer.Thread != target {
				t.Fatalf("answer=%s err=%v", result, err)
			}
			after, _ := driver.control.Member(target)
			if !reflect.DeepEqual(before, after) || inputs != len(host.inputs) || confirmations != len(host.confirmed) || resumes != host.resumes || calls != 1 {
				t.Fatal("side question changed logical work or ordinary host delivery")
			}
			if self, _, _ := driver.control.FeatureUse(caller.Thread); self.Messages != 1 || self.SideQuestions != 1 || self.SideQuestionRejections+self.SideQuestionFailures != 0 {
				t.Fatalf("accepted side question counted %+v", self)
			}
		})
	}
	for _, option := range []string{"", `,"btw":false`} {
		driver, host, _, caller := reconciliationFixture(t)
		caller.Call = "ordinary-message"
		host.ask = func(context.Context, *control.Member, string) (string, error) {
			t.Fatal("ordinary message became a question")
			return "", nil
		}
		before := len(host.inputs)
		if _, err := driver.Call(t.Context(), caller, "send_message", jsontext.Value(`{"target":"child","message":"message"`+option+`}`)); err != nil {
			t.Fatal(err)
		}
		if len(host.inputs) != before+1 || host.inputs[len(host.inputs)-1].Message != "message" {
			t.Fatal("ordinary attributed delivery changed")
		}
		if _, err := driver.Call(t.Context(), caller, "send_message", jsontext.Value(`{"target":"child","message":"message"`+option+`}`)); err != nil {
			t.Fatal(err)
		}
		if self, _, _ := driver.control.FeatureUse(caller.Thread); self.Messages != 1 || self.SideQuestions+self.SideQuestionRejections+self.SideQuestionFailures != 0 {
			t.Fatalf("ordinary message counted %+v; a repeated call must reuse its send", self)
		}
	}
}

func TestSideQuestionFailuresNeverFallBackToInput(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	caller.Call = "question"
	before, resumes := len(host.inputs), host.resumes
	failure := errors.New("native question failed")
	calls := 0
	host.ask = func(context.Context, *control.Member, string) (string, error) {
		calls++
		return "", failure
	}
	args := jsontext.Value(`{"target":"child","message":"question","btw":true}`)
	if _, err := driver.Call(t.Context(), caller, "send_message", args); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, name := range []string{"followup_task", "interrupt_agent"} {
		if _, err := driver.Call(t.Context(), caller, name, args); err == nil {
			t.Fatalf("%s accepted btw", name)
		}
	}
	foreign := caller
	foreign.Session = "foreign"
	if _, err := driver.Call(t.Context(), foreign, "send_message", args); err == nil {
		t.Fatal("foreign caller accepted")
	}
	driver.host = questionWithoutRuntime{host}
	if _, err := driver.Call(t.Context(), caller, "send_message", args); err == nil {
		t.Fatal("target without a live runtime accepted a question")
	}
	driver.host = host
	if calls != 1 || len(host.inputs) != before || host.resumes != resumes {
		t.Fatal("question failure changed target input or resumed the target")
	}
	if self, _, _ := driver.control.FeatureUse(caller.Thread); self.SideQuestions != 1 || self.Messages != 1 || self.SideQuestionFailures != 1 || self.SideQuestionRejections != 1 {
		t.Fatalf("btw counts must separate the dispatched failure from the rejected runtime call, excluding the foreign caller: %+v", self)
	}
	// A question cannot return an answer under a newer logical work generation.
	host.ask = func(ctx context.Context, target *control.Member, _ string) (string, error) {
		if err := driver.control.HostCompleted(ctx, target.Thread, "child-turn", control.Completed, control.HostFailure{}); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.control.PrepareAction(ctx, caller, target.Thread, "followup", "new work"); err != nil {
			t.Fatal(err)
		}
		return "stale", nil
	}
	if _, err := driver.Call(t.Context(), caller, "send_message", args); err == nil {
		t.Fatal("changed target generation accepted stale answer")
	}
	if self, _, _ := driver.control.FeatureUse(caller.Thread); self.SideQuestions != 2 || self.SideQuestionFailures != 2 {
		t.Fatalf("discarded answer lost its accepted btw call or failure: %+v", self)
	}
}
