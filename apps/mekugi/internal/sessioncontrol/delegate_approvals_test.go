package sessioncontrol

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

type delegateApprovalResult struct {
	choice prompt.Choice
	reason string
	err    error
}

func TestDelegateApprovalDecisionsUseOnlyNativeReply(t *testing.T) {
	for _, approve := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			owner := NewDelegateApprovals(t.Context())
			defer owner.Close()
			choice := prompt.Choice{ID: 4, Approve: approve, Deny: !approve, Outcome: "Approved"}
			if !approve {
				choice.Outcome = "Denied"
			}
			original := &prompt.Approval{Thread: "child", Turn: "child-turn", Item: "native-tool", Choices: []prompt.Choice{choice}}
			result := make(chan delegateApprovalResult, 1)
			go func() {
				c, reason, err := owner.Request(t.Context(), observation.Thread{ID: original.Thread, Path: "/root/child"}, original)
				result <- delegateApprovalResult{c, reason, err}
			}()
			request := <-owner.Requests()
			request.Approval.ID = "local/1"
			request.Approval.Choices[0].Label = "local label"
			if original.ID != "" || original.Choices[0].Label != "" {
				t.Fatal("presentation changed the requesting runtime's prompt")
			}
			approval := &Approval{Approval: request.Approval, Delegate: request}
			outcome, delivered, err := approval.Decide(choice, " human reason ", ApprovalGrants{}, nil, nil)
			if err != nil || !delivered || outcome == "" {
				t.Fatalf("decision = %q, %v, %v", outcome, delivered, err)
			}
			got := <-result
			if got.err != nil || got.choice.Approve != approve || got.reason != "human reason" || !request.Finished() {
				t.Fatalf("native reply = %+v, finished = %v", got, request.Finished())
			}
			if _, delivered, err := approval.Decide(choice, "again", ApprovalGrants{}, nil, nil); err != nil || delivered {
				t.Fatal("the same request received a second decision")
			}
		})
	}
}

func TestDelegateApprovalCancellationAndLifetime(t *testing.T) {
	for _, closeOwner := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			owner := NewDelegateApprovals(t.Context())
			defer owner.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, _, err := owner.Request(ctx, observation.Thread{ID: "child", Path: "/root/child"}, &prompt.Approval{Thread: "child"})
				result <- err
			}()
			request := <-owner.Requests()
			time.Sleep(10 * time.Minute)
			if request.Finished() {
				t.Fatal("a live request expired without a decision or cancellation")
			}
			if closeOwner {
				owner.Close()
			} else {
				cancel()
			}
			if !request.Finished() || request.Reply(prompt.Choice{Approve: true}, "") {
				t.Fatal("canceled authority accepted a reply")
			}
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request = %v", err)
			}
			<-request.Done
		})
	}
}

func TestDelegateApprovalCanceledBeforeDelivery(t *testing.T) {
	owner := NewDelegateApprovals(t.Context())
	defer owner.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := owner.Request(ctx, observation.Thread{ID: "child", Path: "/root/child"}, &prompt.Approval{Thread: "child"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	select {
	case <-owner.Requests():
		t.Fatal("canceled request was retained for a later dock")
	default:
	}
}
