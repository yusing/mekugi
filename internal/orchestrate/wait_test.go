package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"testing"
	"time"
)

func TestWaitDurableLifecycle(t *testing.T) {
	s, workspace, _ := launchFixture(t)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	register := func(id string, timeout int) Wait {
		t.Helper()
		w, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: id, Targets: []string{"batch"}, TimeoutMS: timeout}, now)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	event := WaitEvent{ID: "event", Task: "batch", Data: jsontext.Value(`{"kind":"done"}`)}
	// No deadline is independent of request cancellation and restart.
	ctx, cancel := context.WithCancel(t.Context())
	w, err := s.RegisterWait(ctx, workspace, "main", Wait{ID: "infinite", Targets: []string{"batch"}}, now)
	if err != nil || w.State != "registered" || !w.Deadline.IsZero() {
		t.Fatal(w, err)
	}
	cancel()
	s = &Store{Directory: s.Directory}
	ready, deadline, err := s.AdvanceWaits(t.Context(), workspace, "main", nil, now.Add(24*time.Hour))
	if err != nil || len(ready) != 0 || !deadline.IsZero() {
		t.Fatal(ready, deadline, err)
	}
	if w := register("infinite", 0); w.State != "registered" {
		t.Fatal(w)
	}
	if _, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: "infinite", TimeoutMS: 1}, now); err == nil {
		t.Fatal("changed duplicate admitted")
	}
	if w, err := s.UpdateWait(t.Context(), workspace, "main", "infinite", "canceled", ""); err != nil || w.State != "canceled" {
		t.Fatal(w, err)
	}
	// Expiry publishes one asynchronous result and cannot consume a later event.
	if w := register("finite", 1); w.State != "registered" || !w.Deadline.Equal(now.Add(time.Millisecond)) {
		t.Fatal(w)
	}
	ready, _, err = s.AdvanceWaits(t.Context(), workspace, "main", []WaitEvent{event}, now.Add(time.Millisecond))
	if err != nil || len(ready) != 1 || !ready[0].TimedOut || ready[0].EventID != "" {
		t.Fatal(ready, err)
	}
	if w := register("observe", 0); w.State != "completed" || w.EventID != event.ID {
		t.Fatal(w)
	}
	if w := register("observe", 0); w.EventID != event.ID {
		t.Fatal("disconnected immediate response lost", w)
	}
	s = &Store{Directory: s.Directory}
	if again, _, err := s.AdvanceWaits(t.Context(), workspace, "main", nil, now); err != nil || len(again) != 0 {
		t.Fatal("uncertain dispatch retried", again, err)
	}
	if _, err = s.UpdateWait(t.Context(), workspace, "main", "finite", "canceled", ""); err == nil {
		t.Fatal("uncertain result released")
	}
	if _, err = s.UpdateWait(t.Context(), workspace, "main", "finite", "delivered", ""); err != nil {
		t.Fatal(err)
	}
	if w := register("finite", 1); w.State != "delivered" {
		t.Fatal(w)
	}
	if ready, _, err = s.AdvanceWaits(t.Context(), workspace, "main", []WaitEvent{event}, now); err != nil || len(ready) != 0 {
		t.Fatal("duplicate observation delivered", ready, err)
	}
}

func TestWaitCancelPreservesUnsentEventAndScope(t *testing.T) {
	s, workspace, _ := launchFixture(t)
	now := time.Now()
	input := Wait{ID: "selected", Targets: []string{"batch"}}
	if _, err := s.RegisterWait(t.Context(), workspace, "main", input, now); err != nil {
		t.Fatal(err)
	}
	other := WaitEvent{ID: "other", Task: "other", Data: jsontext.Value(`{"kind":"message"}`)}
	event := WaitEvent{ID: "prompt", Task: "batch", Data: jsontext.Value(`{"kind":"approval"}`)}
	ready, _, err := s.AdvanceWaits(t.Context(), workspace, "main", []WaitEvent{other, event}, now)
	if err != nil || len(ready) != 1 || ready[0].EventID != "prompt" {
		t.Fatal(ready, err)
	}
	if _, err := s.UpdateWait(t.Context(), workspace, "main", input.ID, "ready", ""); err != nil {
		t.Fatal(err)
	}
	if w, err := s.UpdateWait(t.Context(), workspace, "main", input.ID, "canceled", ""); err != nil || w.State != "canceled" {
		t.Fatal(w, err)
	}
	for _, id := range []string{"other", "prompt"} {
		w, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: "observe-" + id}, now)
		if err != nil || w.EventID != id {
			t.Fatal(w, err)
		}
	}
	if _, err := s.UpdateWait(t.Context(), workspace, "foreign", input.ID, "canceled", ""); err == nil {
		t.Fatal("foreign cancellation")
	}
	if _, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: "unknown", Targets: []string{"unknown"}}, now); err == nil {
		t.Fatal("unknown selection")
	}
}

func TestWaitRecoveryReconcilesInterruptedDelivery(t *testing.T) {
	s, workspace, _ := launchFixture(t)
	now := time.Now()
	for _, id := range []string{"lost", "acknowledged"} {
		if _, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: id}, now); err != nil {
			t.Fatal(err)
		}
	}
	events := []WaitEvent{
		{ID: "done", Task: "batch", Data: jsontext.Value(`{"kind":"done"}`)},
		{ID: "late", Task: "batch", Data: jsontext.Value(`{"kind":"done"}`)},
		{ID: "approval", Task: "batch", Data: jsontext.Value(`{"kind":"approval"}`), Prompt: true},
		{ID: "message", Task: "batch", Data: jsontext.Value(`{"kind":"message"}`)},
	}
	if ready, _, err := s.AdvanceWaits(t.Context(), workspace, "main", events, now); err != nil || len(ready) != 2 {
		t.Fatal(ready, err)
	}
	// A restart interrupts both deliveries and the unclaimed live prompt.
	s = &Store{Directory: s.Directory}
	if err := s.RecoverWaits(t.Context(), workspace, "main"); err != nil {
		t.Fatal(err)
	}
	unsettled, err := s.Waits(workspace, "main", "uncertain", "rejected")
	if err != nil || len(unsettled) != 2 || unsettled[0].State != "uncertain" {
		t.Fatal(unsettled, err)
	}
	if _, err := s.UpdateWait(t.Context(), workspace, "main", "acknowledged", "delivered", ""); err != nil {
		t.Fatal("late acknowledgement rejected", err)
	}
	if w, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: "acknowledged"}, now); err != nil || w.State != "delivered" || w.Event != nil {
		t.Fatal("delivered result repeated", w, err)
	}
	if w, err := s.UpdateWait(t.Context(), workspace, "main", "acknowledged", "canceled", ""); err != nil || w.Event != nil {
		t.Fatal("cancel repeated a delivered event", w, err)
	}
	s = &Store{Directory: s.Directory}
	if history, err := s.Waits(workspace, "main", "ready", "dispatching", "delivered", "uncertain", "rejected", "canceled"); err != nil || len(history) != 2 || history[1].EventID != "late" || string(history[1].Event) != string(events[1].Data) {
		t.Fatal("restart lost the selected result's presentation evidence", history, err)
	}
	if w, err := s.UpdateWait(t.Context(), workspace, "main", "lost", "canceled", ""); err != nil || w.State != "canceled" {
		t.Fatal(w, err)
	}
	if unsettled, err = s.Waits(workspace, "main", "uncertain", "rejected"); err != nil || len(unsettled) != 0 {
		t.Fatal(unsettled, err)
	}
	// Main reported the lost result absent, so a later wait receives it; the
	// stale prompt is gone and the remaining message stays queued.
	for i, want := range []string{"done", "message"} {
		if w, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: fmt.Sprint("next-", i)}, now); err != nil || w.EventID != want {
			t.Fatal(want, w, err)
		}
	}
	if w, err := s.RegisterWait(t.Context(), workspace, "main", Wait{ID: "empty"}, now); err != nil || w.State != "registered" {
		t.Fatal("dropped prompt or delivered event returned", w, err)
	}
}
