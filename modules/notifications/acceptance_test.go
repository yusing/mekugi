package notifications_test

import (
	"fmt"
	"reflect"
	"slices"
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
	got := slices.Clone(batch.Messages)
	for i := range got {
		got[i].Occurred = time.Time{}
	}
	if !reflect.DeepEqual(got, want) {
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

func TestRequestSuccessClearsOnlyOriginatingErrors(t *testing.T) {
	service := newService(t)
	now := time.Now()
	for _, thread := range []string{"root", "child"} {
		service.Submit(notice.Input{ID: thread, Session: "session", Thread: thread, Category: thread + ":failure", Text: thread + " failed", Request: true})
	}
	var feedback presentation.Model
	feedback.Receive(requireClaim(t, service, map[string]string{"child": "Worker"}), now)
	service.RequestSucceeded("other", now.Add(time.Second))
	if feedback.ClearSucceeded(service, "root") {
		t.Fatal("foreign success cleared errors")
	}
	service.RequestSucceeded("root", now.Add(2*time.Second))
	if !feedback.ClearSucceeded(service, "root") || !feedback.Alert || strings.Contains(feedback.Text, "root failed") || !strings.Contains(feedback.Text, "child failed") {
		t.Fatalf("partial error clearing: %+v", feedback)
	}
	service.RequestSucceeded("child", now.Add(3*time.Second))
	if !feedback.ClearSucceeded(service, "root") || feedback.Alert || feedback.Text != "" || len(service.Pending()) != 0 {
		t.Fatalf("successful requests retained errors: %+v", feedback)
	}
	feedback.Set("new error", true, now.Add(4*time.Second))
	feedback.BindThread("root")
	if feedback.ClearSucceeded(service, "child") {
		t.Fatal("old success or display navigation cleared a new error")
	}
	feedback.Set("saved", false, now)
	service.RequestSucceeded("root", now.Add(5*time.Second))
	if feedback.ClearSucceeded(service, "root") || feedback.Text != "saved" {
		t.Fatal("success cleared ordinary feedback")
	}
}

func TestRequestSuccessDuringNoticeRetry(t *testing.T) {
	service := newService(t)
	service.Submit(notice.Input{ID: "failed", Session: "session", Thread: "root", Turn: "old", Category: "failure", Text: "old failure", Request: true})
	service.ObserveTurn("root", "old", "failed")
	var feedback presentation.Model
	first := requireClaim(t, service, nil)
	feedback.Receive(first, time.Now())
	first.Errors.Finish(false)
	retry := requireClaim(t, service, nil)
	service.RequestSucceeded("root", time.Now())
	feedback.Receive(retry, time.Now())
	feedback.ClearSucceeded(service, "root")
	if feedback.Alert {
		t.Fatalf("retry resurrected retired feedback: %+v", feedback)
	}
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
				requireMessages(t, delivery.Errors, notice.Message{ID: "request", Thread: "root", Text: "complete failure"})
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
				requireMessages(t, previous.Errors, notice.Message{ID: "request", Thread: "root", Text: "failure"})
				previous.Errors.Finish(false)
				if service.Claim("root", nil) != nil {
					t.Fatal("coalesced request escaped before its current turn outcome")
				}
				service.ObserveTurn("root", "second", "failed")
			}
			delivery := requireClaim(t, service, nil)
			requireMessages(t, delivery.Errors, notice.Message{ID: "request", Thread: "root", Text: "failure (occurred 2 times)"})
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
	requireMessages(t, first.Errors, notice.Message{ID: "error", Thread: "root", Text: "storage unavailable"})
	first.Errors.Finish(true)
	if service.Claim("root", nil) != nil {
		t.Fatal("acknowledging errors released progress or redisplayed a repeat")
	}
	first.Progress.Finish(false)
	retry := requireClaim(t, service, nil)
	requireMessages(t, retry.Errors)
	requireMessages(t, retry.Progress, notice.Message{ID: "progress", Thread: "root", Text: "cleanup delayed"})
	retry.Progress.Finish(true)
	if got := service.Pending(); !reflect.DeepEqual(got, []string{"mekugi: storage unavailable (occurred 2 times)"}) {
		t.Fatalf("post-delivery recovery = %v", got)
	}
	if service.Claim("root", nil) != nil {
		t.Fatal("delivered repeats interrupted the view again")
	}
}

func TestWaitingReportsUnclaimedSurface(t *testing.T) {
	service := newService(t)
	service.Submit(notice.Input{ID: "progress", Thread: "root", Category: "context", Text: "context usage", Surface: notice.Progress})
	held := requireClaim(t, service, nil)
	if service.Waiting("root", nil, notice.Error) || service.Waiting("root", nil, notice.Progress) {
		t.Fatal("held progress reported as waiting")
	}
	service.Submit(notice.Input{ID: "other", Thread: "other", Category: "storage", Text: "outside scope"})
	if service.Waiting("root", nil, notice.Error) {
		t.Fatal("out-of-scope error reported as waiting")
	}
	service.Submit(notice.Input{ID: "error", Thread: "root", Category: "storage", Text: "storage unavailable"})
	if !service.Waiting("root", nil, notice.Error) || service.Waiting("root", nil, notice.Progress) {
		t.Fatal("new error behind held progress was not waiting")
	}
	held.Progress.Finish(false)
	retry := requireClaim(t, service, nil)
	requireMessages(t, retry.Errors, notice.Message{ID: "error", Thread: "root", Text: "storage unavailable"})
	requireMessages(t, retry.Progress, notice.Message{ID: "progress", Thread: "root", Text: "context usage"})
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
		notice.Message{ID: "root", Thread: "root", Text: "own failure"},
		notice.Message{ID: "child", Thread: "child", Text: "Worker: child failure"},
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
	requireMessages(t, requireClaim(t, service, nil).Errors, notice.Message{ID: "0", Thread: "root", Text: "late 0"})
}

func TestAttachedFeedbackRetryAndLifetime(t *testing.T) {
	service := newService(t)
	complete := strings.Repeat("complete detail\n", 100)
	service.Submit(notice.Input{ID: "error", Thread: "root", Category: "error", Text: complete})
	service.Submit(notice.Input{ID: "progress", Thread: "root", Category: "progress", Text: "cleanup", Surface: notice.Progress})
	var view presentation.Model
	var transcript presentation.Progress
	var added []notice.Message
	add := func(message notice.Message) uint64 {
		added = append(added, message)
		return uint64(len(added))
	}
	present := func(uint64) bool { return true }
	now := time.Unix(100, 0)
	first := requireClaim(t, service, nil)
	view.Receive(first, now)
	if got := transcript.Rows(first.Progress, present, add); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("first progress rows = %v, added = %v", got, added)
	}
	requireMessages(t, notice.Batch{Messages: added}, notice.Message{ID: "progress", Thread: "root", Text: "cleanup"})
	if view.Text != complete || !view.Alert || view.Expire(now.Add(time.Hour)) {
		t.Fatal("complete actionable feedback was truncated or expired")
	}
	view.Set("replacement", false, now)
	first.Errors.Finish(false)
	first.Progress.Finish(false)
	retry := requireClaim(t, service, nil)
	view.Receive(retry, now)
	if view.Text != complete {
		t.Fatal("retry did not restore the complete error")
	}
	// Another view of the same transcript names the existing row.
	if got := transcript.Rows(retry.Progress, present, add); !reflect.DeepEqual(got, []uint64{1}) || len(added) != 1 {
		t.Fatalf("retried progress rows = %v, added = %v", got, added)
	}
	var replaced presentation.Progress
	if got := replaced.Rows(retry.Progress, present, add); !reflect.DeepEqual(got, []uint64{2}) {
		t.Fatal("progress delivery identity escaped its transcript")
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
