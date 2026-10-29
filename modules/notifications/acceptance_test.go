package notifications_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/notifications"
	"github.com/yusing/mekugi/modules/notifications/notice"
	"github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func newService(t *testing.T) *notice.Service {
	t.Helper()
	return moduletest.New(t, notifications.Module, notifications.Deps{}, module.Slots{}, new(moduletest.Scripted))
}

func requireMessages(t *testing.T, batch notice.Batch, want ...notice.Message) {
	t.Helper()
	if !reflect.DeepEqual(batch.Messages, want) {
		t.Fatalf("messages = %+v, want %+v", batch.Messages, want)
	}
}

func requireClaim(t *testing.T, service *notice.Service, descendants map[string]string) *notice.Delivery {
	t.Helper()
	delivery := service.Claim("root", descendants)
	if delivery == nil {
		t.Fatal("expected a delivery claim")
	}
	return delivery
}

func TestRequestOutcomeAcceptance(t *testing.T) {
	for _, status := range []string{"failed", "completed", "interrupted"} {
		for _, late := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/late=%t", status, late), func(t *testing.T) {
				service := newService(t)
				input := notice.Input{ID: "request", Session: "session", Thread: "root", Turn: "turn", Category: "provider", Reference: "diagnostic", Text: "complete failure", Request: true}
				if late {
					service.ObserveTurn("root", "turn", status)
				}
				service.Submit(input)
				service.Submit(input) // The same diagnostic is a retry, not another cause.
				if !late {
					service.ObserveTurn("root", "other", "failed")
					service.ObserveTurn("root", "turn", "running")
					if service.Claim("root", nil) != nil {
						t.Fatal("request escaped before its terminal outcome")
					}
					if got := service.Pending(); !reflect.DeepEqual(got, []string{"mekugi: complete failure"}) {
						t.Fatalf("unresolved recovery = %v", got)
					}
					service.ObserveTurn("root", "turn", status)
				}
				if status != "failed" {
					if service.Claim("root", nil) != nil || len(service.Pending()) != 0 {
						t.Fatal("successful or interrupted request retained an error")
					}
					return
				}
				delivery := requireClaim(t, service, nil)
				requireMessages(t, delivery.Errors, notice.Message{ID: "request", Text: "complete failure"})
				delivery.Errors.Finish(true)
				if len(service.Pending()) != 0 {
					t.Fatal("visible failure remained pending")
				}
			})
		}
	}
}

func TestCoalescedRequestOutcomeGate(t *testing.T) {
	for _, priorFailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_failed=%t", priorFailed), func(t *testing.T) {
			service := newService(t)
			input := notice.Input{ID: "request", Session: "session", Thread: "root", Turn: "first", Category: "provider", Reference: "first", Text: "failure", Request: true}
			if priorFailed {
				service.ObserveTurn("root", "first", "failed")
			} else {
				service.ObserveTurn("root", "second", "failed")
			}
			service.Submit(input)
			var previous *notice.Delivery
			if priorFailed {
				previous = requireClaim(t, service, nil)
			}
			input.Turn, input.Reference = "second", "second"
			service.Submit(input)
			if priorFailed {
				if service.Claim("root", nil) != nil {
					t.Fatal("coalescing released an in-flight claim")
				}
				requireMessages(t, previous.Errors, notice.Message{ID: "request", Text: "failure"})
				previous.Errors.Finish(false)
				if service.Claim("root", nil) != nil {
					t.Fatal("coalesced request escaped before its current turn outcome")
				}
				service.ObserveTurn("root", "second", "failed")
			}
			delivery := requireClaim(t, service, nil)
			requireMessages(t, delivery.Errors, notice.Message{ID: "request", Text: "failure (occurred 2 times)"})
			delivery.Errors.Finish(true)
			if len(service.Pending()) != 0 || service.Claim("root", nil) != nil {
				t.Fatal("acknowledged coalesced notice remained pending")
			}
		})
	}
}

func TestIndependentSurfaceReceiptsAndCapturedRepeats(t *testing.T) {
	service := newService(t)
	err := notice.Input{ID: "error", Thread: "root", Category: "storage", Text: "storage unavailable"}
	progress := notice.Input{ID: "progress", Thread: "root", Category: "cleanup", Text: "cleanup delayed", Surface: notice.Progress}
	service.Submit(err)
	service.Submit(progress)
	first := requireClaim(t, service, nil)
	if service.Claim("root", nil) != nil {
		t.Fatal("claimed entries were reserved twice")
	}
	service.Submit(err)
	requireMessages(t, first.Errors, notice.Message{ID: "error", Text: "storage unavailable"})
	first.Errors.Finish(true)
	if service.Claim("root", nil) != nil {
		t.Fatal("acknowledging errors released progress or redisplayed a repeat")
	}
	first.Progress.Finish(false)
	retry := requireClaim(t, service, nil)
	requireMessages(t, retry.Errors)
	requireMessages(t, retry.Progress, notice.Message{ID: "progress", Text: "cleanup delayed"})
	retry.Progress.Finish(true)
	if got := service.Pending(); !reflect.DeepEqual(got, []string{"mekugi: storage unavailable (occurred 2 times)"}) {
		t.Fatalf("post-delivery recovery = %v", got)
	}
	if service.Claim("root", nil) != nil {
		t.Fatal("delivered repeats interrupted the view again")
	}
}

func TestNormalizedScopeAcceptance(t *testing.T) {
	service := newService(t)
	for _, input := range []notice.Input{
		{ID: "root", Thread: "root", Text: "own failure"},
		{ID: "child", Thread: "child", Text: "child failure"},
		{ID: "aux", Category: "auxiliary", Text: "invocation failure"},
		{ID: "other", Thread: "other", Text: "other root"},
		{ID: "unknown", Session: "unresolved", Text: "unknown thread"},
	} {
		service.Submit(input)
	}
	if service.Claim("", map[string]string{"child": "Worker"}) != nil {
		t.Fatal("missing root acquired notices")
	}
	delivery := requireClaim(t, service, map[string]string{"child": "Worker"})
	requireMessages(t, delivery.Errors,
		notice.Message{ID: "root", Text: "own failure"},
		notice.Message{ID: "child", Text: "Worker: child failure"},
		notice.Message{ID: "aux", Text: "invocation failure"})
	delivery.Errors.Finish(true)
	if got := service.Pending(); !reflect.DeepEqual(got, []string{"mekugi: other root", "mekugi: unknown thread"}) {
		t.Fatalf("unproven scope recovery = %v", got)
	}
}

func TestBoundedRecoveryAndRecentOutcomes(t *testing.T) {
	service := newService(t)
	for i := range 259 {
		service.Submit(notice.Input{ID: fmt.Sprint(i), Category: fmt.Sprint(i), Thread: "root", Text: "failure"})
	}
	pending := service.Pending()
	if len(pending) != 257 || pending[256] != "mekugi: 3 additional failures could not be retained; check the failed turns." {
		t.Fatalf("bounded recovery = %v", pending)
	}
	delivery := requireClaim(t, service, nil)
	if len(delivery.Errors.Messages) != 256 {
		t.Fatalf("retained notices = %d", len(delivery.Errors.Messages))
	}
	delivery.Errors.Finish(true)
	if got := service.Pending(); !reflect.DeepEqual(got, pending[256:]) {
		t.Fatalf("overflow lost after delivery: %v", got)
	}

	service = newService(t)
	for i := range 257 {
		service.ObserveTurn("root", fmt.Sprint(i), "completed")
	}
	for _, turn := range []string{"0", "1", "256"} {
		service.Submit(notice.Input{ID: turn, Thread: "root", Turn: turn, Category: turn, Text: "late " + turn, Request: true})
	}
	if got := service.Pending(); !reflect.DeepEqual(got, []string{"mekugi: late 0"}) {
		t.Fatalf("recent outcome retention = %v", got)
	}
	if service.Claim("root", nil) != nil {
		t.Fatal("evicted outcome was treated as a known failure")
	}
	service.ObserveTurn("root", "0", "failed")
	requireMessages(t, requireClaim(t, service, nil).Errors, notice.Message{ID: "0", Text: "late 0"})
}

func TestAttachedFeedbackRetryAndLifetime(t *testing.T) {
	service := newService(t)
	complete := strings.Repeat("complete detail\n", 100)
	service.Submit(notice.Input{ID: "error", Thread: "root", Category: "error", Text: complete})
	service.Submit(notice.Input{ID: "progress", Thread: "root", Category: "progress", Text: "cleanup", Surface: notice.Progress})
	var view presentation.Model
	now := time.Unix(100, 0)
	first := requireClaim(t, service, nil)
	if got := view.Receive(first, now); !reflect.DeepEqual(got, []notice.Message{{ID: "progress", Text: "cleanup"}}) {
		t.Fatalf("first progress = %v", got)
	}
	if view.Text != complete || !view.Alert || view.Expire(now.Add(time.Hour)) {
		t.Fatal("complete actionable feedback was truncated or expired")
	}
	view.Set("replacement", false, now)
	first.Errors.Finish(false)
	first.Progress.Finish(false)
	retry := requireClaim(t, service, nil)
	if got := view.Receive(retry, now); len(got) != 0 || view.Text != complete {
		t.Fatalf("retry progress = %v, complete error restored = %t", got, view.Text == complete)
	}
	var reattached presentation.Model
	if got := reattached.Receive(retry, now); len(got) != 1 {
		t.Fatal("progress delivery identity escaped its attached view")
	}
	retry.Errors.Finish(true)
	retry.Progress.Finish(true)
	view.Set("saved", false, now)
	if view.Expire(now.Add(3*time.Second-time.Nanosecond)) || !view.Expire(now.Add(3*time.Second)) || view.Text != "" {
		t.Fatal("non-actionable feedback did not expire at three seconds")
	}
}

func TestDisabledNoticeService(t *testing.T) {
	var service *notice.Service
	service.Submit(notice.Input{Text: "failure"})
	service.ObserveTurn("root", "turn", "failed")
	if service.Claim("root", nil) != nil || len(service.Pending()) != 0 {
		t.Fatal("disabled service produced notices")
	}
	var batch *notice.Batch
	batch.Finish(true)
}
