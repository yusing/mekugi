package router

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

const journalContinuationPrefix = "mekugi-journal-continue-"

type journalResetIntent struct {
	ID         string `json:"id"`
	Turn       string `json:"turn"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	Phase      string `json:"phase"` // pending, armed, consumed, starting, cancelled, failed, unknown
	ResponseID string `json:"response_id,omitempty"`
	Resume     bool   `json:"resume,omitzero"` // Unfinished work, not a slice boundary; never compact.
}

func (s *journalStore) beginJournalTurn(ctx context.Context, store *mekugiReplayStore, workspace, thread, turn string) error {
	if turn == "" {
		return nil
	}
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || j.TurnID == turn && !j.WorkPaused && !j.hasPausedWorkTimer() {
			return errJournalUnchanged
		}
		if j.TurnID != turn {
			j.TurnID, j.TurnStartSeq = turn, j.Sequence
			j.Turns++
		}
		j.setWorkTimers(true, time.Now())
		return nil
	})
}

// completedSlice selects a slice boundary or ordinary unfinished work. It is
// called only by a driver with no live intent, so a retained
// intent is uncertain-outcome evidence that was already reported. It is replaced
// rather than blocking every later slice of the thread.
func (s *journalStore) completedSlice(ctx context.Context, store *mekugiReplayStore, workspace, thread, turn string) (*journalResetIntent, error) {
	var intent *journalResetIntent
	err := s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || turn == "" ||
			j.TurnID != turn || j.ResetHandledTurn == turn {
			return errJournalUnchanged
		}
		j.ResetHandledTurn, j.ResetIntent = turn, nil
		if next := j.continuationCandidate(turn); next != nil {
			next.ID = rand.Text()
			j.ResetIntent = next
			copy := *next
			intent = &copy
		}
		return nil
	})
	return intent, err
}

// Select from candidate journal facts without handling a turn or allocating an
// intent ID. Main continuation owns its root gate; finish checks each caller's work.
func (j *threadJournal) continuationCandidate(turn string) *journalResetIntent {
	// A blocker is an explicit stop, not an invitation to try another turn.
	if j.continuationBlocker() != nil {
		return nil
	}
	if turn != "" && j.TurnID == turn {
		for _, event := range j.Events {
			if event.Seq <= j.TurnStartSeq || !event.Transition || event.Fields.State != "done" || !j.SliceParents[journalParent(event.Path)] {
				continue
			}
			i := j.treeIndex(event.Path)
			if i < 0 || j.Items[i].State != "done" {
				continue
			}
			for _, item := range j.Items {
				if item.State == "pending" && journalParent(item.Path) == journalParent(event.Path) && j.localJournalTask(item.Path) {
					return &journalResetIntent{Turn: turn, Path: item.Path, Title: item.Title, Phase: "pending"}
				}
			}
		}
	}
	// Prefer work already in progress, including an unchanged follow-up turn.
	for _, state := range []string{"working", "pending"} {
		for _, item := range j.Items {
			if item.State == state && j.runnableJournalTask(item.Path) {
				return &journalResetIntent{Turn: turn, Path: item.Path, Title: item.Title, Phase: "pending", Resume: true}
			}
		}
	}
	return nil
}

// Mounted child state is evidence, not an owned task that pauses this journal.
func (j *threadJournal) continuationBlocker() *journalItem {
	var blocker *journalItem
	for i := range j.Items {
		item := &j.Items[i]
		if item.Kind == "task" && item.State == "blocked" && !strings.Contains(item.Path, "/@") && (blocker == nil || item.Updated > blocker.Updated) {
			blocker = item
		}
	}
	return blocker
}

// Slice targets may have open children, but share ordinary work's ownership
// and ancestor disposition checks.
func (j *threadJournal) localJournalTask(path string) bool {
	i := j.treeIndex(path)
	if i < 0 || j.StoppedTasks[path] != "" || j.Items[i].Kind != "task" || (j.Items[i].State != "pending" && j.Items[i].State != "working") {
		return false
	}
	for _, item := range j.Items {
		if item.Kind != "task" {
			continue
		}
		if item.Path == path || strings.HasPrefix(path, item.Path+"/") {
			if item.Agent != "" || item.State == "done" || item.State == "dropped" {
				return false
			}
		}
	}
	return true
}

// A parent waiting for unfinished children is not itself runnable.
func (j *threadJournal) runnableJournalTask(path string) bool {
	if !j.localJournalTask(path) {
		return false
	}
	for _, item := range j.Items {
		if item.Kind == "task" && strings.HasPrefix(item.Path, path+"/") && (item.State == "pending" || item.State == "working") {
			return false
		}
	}
	return true
}

func (j *threadJournal) continuationCurrent(intent *journalResetIntent) bool {
	if !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.StoppedTasks[intent.Path] != "" || j.continuationBlocker() != nil {
		return false
	}
	if intent.Resume {
		return intent.Turn == j.TurnID && j.runnableJournalTask(intent.Path)
	}
	i := j.treeIndex(intent.Path)
	return i >= 0 && j.Items[i].State == "pending" && j.localJournalTask(intent.Path)
}

// Retain the user's stop before requesting an interrupt. The host can race and
// successfully finish instead; that is still a user stop, including after resume.
func (s *journalStore) stopJournalTurn(ctx context.Context, store *mekugiReplayStore, workspace, thread, turn string) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || turn == "" || j.TurnID != turn {
			return errJournalUnchanged
		}
		j.setWorkTimers(false, time.Now())
		j.ResetHandledTurn, j.ResetIntent = turn, nil
		j.StoppedTasks = make(map[string]string)
		for _, item := range j.Items {
			if item.Kind == "task" && (item.State == "pending" || item.State == "working") {
				j.StoppedTasks[item.Path] = turn
			}
		}
		return nil
	})
}

func (s *journalStore) changeReset(ctx context.Context, store *mekugiReplayStore, workspace, thread, id string, mutate func(*threadJournal, *journalResetIntent) error) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || j.ResetIntent == nil || j.ResetIntent.ID != id {
			return errors.New("slice continuation intent is no longer current")
		}
		return mutate(j, j.ResetIntent)
	})
}

// restorableReset returns an intent that a restarted frontend may replay: a
// not-yet-dispatched countdown for the journal's latest turn whose next slice is
// still pending. Any other retained intent is removed so it cannot block later
// slices or answer an unrelated compaction; interrupted reports whether it
// carried uncertain dispatch evidence the user must be told about.
func (s *journalStore) restorableReset(ctx context.Context, store *mekugiReplayStore, workspace, thread string) (intent *journalResetIntent, interrupted bool, err error) {
	err = s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || j.ResetIntent == nil {
			return errJournalUnchanged
		}
		current := j.ResetIntent
		if current.Phase == "pending" {
			if current.Turn == j.TurnID && j.continuationCurrent(current) {
				copy := *current
				intent = &copy
				return errJournalUnchanged
			}
		} else {
			interrupted = true
		}
		j.ResetIntent = nil
		return nil
	})
	return intent, interrupted, err
}

func (s *mekugiReplayStore) resetIntent(ctx context.Context, workspace, thread string) (*journalResetIntent, error) {
	var intent *journalResetIntent
	err := s.locked(ctx, func() error {
		j, exists, err := readThreadJournal(s, workspace, thread)
		if err != nil || !exists {
			return err
		}
		if j.ResetIntent != nil {
			copy := *j.ResetIntent
			intent = &copy
		}
		return nil
	})
	return intent, err
}

func journalContinuationText(intent *journalResetIntent) string {
	return fmt.Sprintf("Continue the journal plan: %s %s.", intent.Path, intent.Title)
}
