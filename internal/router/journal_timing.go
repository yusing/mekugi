package router

import (
	"context"
	"crypto/rand"
	"time"
)

// A durable clock anchor belongs to this process only. Recovery retains the
// last checkpoint, not unobserved time while the router was absent.
var journalTimerOwner = rand.Text()

func (j *threadJournal) discardForeignTimerAnchors() {
	if j.TimerOwner == journalTimerOwner {
		return
	}
	for i := range j.Items {
		j.Items[i].WorkTimer.Since = time.Time{}
	}
}

func (j *threadJournal) checkpointWorkTimers(now time.Time) {
	for i := range j.Items {
		timer := &j.Items[i].WorkTimer
		if !timer.Since.IsZero() {
			timer.update(true, now)
		}
	}
}

func (j *threadJournal) setWorkTimers(running bool, now time.Time) {
	j.WorkPaused = !running
	for i := range j.Items {
		item := &j.Items[i]
		item.WorkTimer.update(running && item.Kind == "task" && item.State == "working", now)
	}
}

func (j *threadJournal) hasPausedWorkTimer() bool {
	for _, item := range j.Items {
		if item.Kind == "task" && item.State == "working" && item.WorkTimer.Since.IsZero() {
			return true
		}
	}
	return false
}

// Thread status pauses timing without rewriting a child's completion state.
func (s *journalStore) observeWorkStatus(ctx context.Context, store *mekugiReplayStore, workspace, thread string, running bool) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || j.WorkPaused == !running && (!running || !j.hasPausedWorkTimer()) {
			return errJournalUnchanged
		}
		j.setWorkTimers(running, time.Now())
		return nil
	})
}
