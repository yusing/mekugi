// Source: apps/mekugi/internal/launch/web_prompts_test.go:78:109,201:287@[14a6d1f3]
package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAPIQuestionBatchRejectsAtomically(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "batch", "Who receives it?", "Who approves it?")
	call := apiPrompts(&u.attached, &u.Client).Questions[0]
	before := u.attached.conversation.Questions.Calls[0].Questions[0]
	intent := sessionapi.Intent{Action: sessionapi.AnswerQuestions, Answer: sessionapi.Answer{Call: call.ID,
		Answers: []sessionapi.QuestionAnswer{
			{Question: call.Questions[0].ID, Choice: new(0), Text: "must not persist"},
			{Question: call.Questions[1].ID, Choice: new(99)},
		}}}
	if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
		t.Fatalf("invalid batch refusal = %q, wire = %s", refusal, wire)
	}
	if after := u.attached.conversation.Questions.Calls[0].Questions[0]; !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid later answer changed earlier state: before=%+v after=%+v", before, after)
	}
	intent.Answer.Answers[1].Choice = new(1)
	if refusal := apiTestIntent(t, u, intent); refusal != "" {
		t.Fatalf("corrected batch refused: %s", refusal)
	}
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/steer" || requests[0].Params.ThreadID != "main" || requests[0].Params.ExpectedTurnID != "turn" {
		t.Fatalf("batch did not use Main's ordinary submission: %+v", requests)
	}
	text := requests[0].text()
	if strings.Count(text, binding.QuestionReplyStart) != 1 || !strings.Contains(text, "Customers") || !strings.Contains(text, "Internal") {
		t.Fatalf("batch lost answers: %s", text)
	}
}

func TestAPIQuestionsKeepSecretsAndSidesInTerminal(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	oneoffTestStart(t, u, wire)
	questionTestMessage(t, u, "side-question", "item/tool/requestUserInput", map[string]any{
		"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "ask",
		"questions": []any{map[string]any{"id": "scope", "header": "Side", "question": "Side question?"}},
	})
	questionTestSync(t, u, "secret", true)
	wire.Reset()
	projected := apiPrompts(&u.attached, &u.Client).Questions
	if len(projected) != 1 || len(projected[0].Questions) != 1 {
		t.Fatalf("side question leaked or secret marker missing: %+v", projected)
	}
	q := projected[0].Questions[0]
	if !q.Secret || q.Header != "" || q.Text != "" || len(q.Options) != 0 {
		t.Fatalf("secret content exposed: %+v", q)
	}
	for _, call := range u.attached.conversation.Questions.Calls {
		before := call.View()
		questionBefore := call.Questions[0]
		intent := sessionapi.Intent{Action: sessionapi.AnswerQuestions, Answer: sessionapi.Answer{Call: call.ID,
			Answers: []sessionapi.QuestionAnswer{{Question: before.Questions[0].ID, Text: "browser input"}}}}
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 || !reflect.DeepEqual(call.View(), before) || !reflect.DeepEqual(call.Questions[0], questionBefore) {
			t.Fatalf("terminal-only question accepted browser input: refusal=%q wire=%s", refusal, wire)
		}
	}
}

func TestAPIDecidesApproval(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	approvalTestCommand(t, u, "accept-request", map[string]any{"availableDecisions": []string{"accept", "decline"}})
	approvalTestCommand(t, u, "deny-request", map[string]any{"threadId": "child", "availableDecisions": []string{"accept", "decline"}})
	projected := apiPrompts(&u.attached, &u.Client).Approvals
	if len(projected) != 2 || projected[0].Agent != "" || projected[1].Agent != "/root/worker" {
		t.Fatalf("approval identity/order = %+v", projected)
	}
	for i, a := range projected {
		choice := a.Choices[i]
		intent := sessionapi.Intent{Action: sessionapi.DecideApproval, Decide: sessionapi.Decide{Approval: a.ID, Choice: 99}}
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
			t.Fatalf("unoffered choice refusal = %q, wire = %s", refusal, wire)
		}
		intent.Decide.Choice = choice.ID
		intent.Decide.Reason = "keep this change local"
		if !choice.Deny {
			if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
				t.Fatalf("accept with reason refusal = %q, wire = %s", refusal, wire)
			}
			intent.Decide.Reason = ""
		}
		u.Text = "unrelated terminal draft"
		if refusal := apiTestIntent(t, u, intent); refusal != "" {
			t.Fatalf("decision refused: %s", refusal)
		}
		var reply struct {
			ID     string `json:"id"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
		}
		if err := json.Unmarshal(wire.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		wantID, wantDecision := "accept-request", "accept"
		if choice.Deny {
			wantID, wantDecision = "deny-request", "decline"
			feedback := u.controlObservations.Feedback("child", "turn")
			if !strings.Contains(feedback, intent.Decide.Reason) || strings.Contains(feedback, "unrelated terminal draft") {
				t.Fatalf("denial reason/identity lost: %s", feedback)
			}
		}
		if reply.ID != wantID || reply.Result.Decision != wantDecision {
			t.Fatalf("wrong host decision: %s", wire)
		}
		wire.Reset()
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
			t.Fatalf("repeated decision refusal = %q, wire = %s", refusal, wire)
		}
	}
	if got := apiPrompts(&u.attached, &u.Client).Approvals; len(got) != 0 {
		t.Fatalf("decided approvals remain projected: %+v", got)
	}
}

func TestAPIGuardDecisionsRequireAttachmentForReusableGrants(t *testing.T) {
	for _, kind := range []string{"", "sudo"} {
		t.Run("kind="+kind, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			h := remoteOwnerView(t, u)
			decide := func(value sessionapi.Decide) (int, string) {
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					done <- h.rpc.Call(ctx, "session/decide", sessionapi.RPCParams{Thread: u.Thread, Body: body}, nil)
				}()
				select {
				case intent := <-h.api.Intents():
					intent.Reply <- apiTestIntent(t, u, intent)
					err = <-done
				case err = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if err == nil {
					return http.StatusAccepted, ""
				}
				if failure, ok := errors.AsType[*sessionapi.RPCError](err); ok && failure.Data != nil {
					return failure.Data.Status, failure.Message
				}
				t.Fatal(err)
				return 0, ""
			}
			request := compositionApproval(kind)
			addGuardApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
			approval := u.approvals.calls()[0]
			intent := sessionapi.Intent{Action: sessionapi.DecideApproval, Decide: sessionapi.Decide{Approval: approval.ID, Choice: 1}}
			if code, refusal := decide(intent.Decide); code != http.StatusConflict || !strings.Contains(refusal, "view/intent") || len(request.Reply) != 0 || len(u.approvals.calls()) != 1 || u.attached.grants.Allows(request) {
				t.Fatalf("reusable grant did not preserve pending approval: %q", refusal)
			}
			intent.Decide.Choice = 0
			if code, refusal := decide(intent.Decide); code != http.StatusAccepted || refusal != "" || len(request.Reply) != 1 || !(<-request.Reply).OK {
				t.Fatalf("approve-once refused: %q", refusal)
			}
			repeated := compositionApproval(kind)
			addGuardApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, repeated)
			if len(repeated.Reply) != 0 || len(u.approvals.calls()) != 1 {
				t.Fatal("approve-once granted the repeated command")
			}
			intent.Decide = sessionapi.Decide{Approval: u.approvals.calls()[0].ID, Choice: 2, Reason: "keep local"}
			if code, refusal := decide(intent.Decide); code != http.StatusAccepted || refusal != "" || len(repeated.Reply) != 1 {
				t.Fatalf("denial refused: %q", refusal)
			}
			if reply := <-repeated.Reply; reply.OK || !strings.Contains(reply.Reason, "keep local") {
				t.Fatalf("denial reply=%+v", reply)
			}
		})
	}
}
