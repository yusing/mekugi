package control

import (
	"context"
	"sync"
	"testing"
)

// featureRecorder is the native measurement stand-in for existing producer fixtures.
// It reduces accepted facts only; production Go neither keeps this state nor increments counts.
type featureRecorder struct {
	mu    sync.Mutex
	facts map[[3]string]FeatureFact
	seed  map[string]FeatureUse
}

func newFeatureRecorder() *featureRecorder {
	return &featureRecorder{facts: make(map[[3]string]FeatureFact), seed: make(map[string]FeatureUse)}
}

func (r *featureRecorder) RecordFeature(_ context.Context, fact FeatureFact) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [3]string{fact.Thread, fact.Operation, string(fact.Event.Kind)}
	if _, seen := r.facts[key]; !seen {
		r.facts[key] = fact
	}
}

func (r *featureRecorder) InvalidateFeatureUse(error) {}

func (r *featureRecorder) ReadFeatureUse(_ context.Context, threads []string) (map[string]FeatureUse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]FeatureUse)
	for _, id := range threads {
		result[id] = r.seed[id]
	}
	for _, fact := range r.facts {
		u, selected := result[fact.Thread]
		if !selected {
			continue
		}
		event := fact.Event
		switch event.Kind {
		case FeatureResetCompleted:
			u.Compactions++
		case FeatureSelfResetRequested:
			u.SelfCompactions++
			if event.Preload {
				u.CompactionPreloads++
			}
		case FeatureSelfResetCompleted:
			u.SelfCompactionsCompleted++
		case FeatureSelfResetFailed:
			u.SelfCompactionFailures++
		case FeatureChildAdmitted:
			u.Spawns++
			if event.Preload {
				u.SpawnPreloads++
			}
			if event.Sources {
				u.AssignmentSources++
			}
		case FeaturePreloadRejected:
			u.PreloadRejections++
		case FeatureFinalDelivered:
			u.Finals++
			if event.Changes {
				u.CompletionChanges++
			}
		case FeatureMessageAccepted:
			u.Messages++
			if event.Sources {
				u.MessageSources++
			}
			if event.BTW {
				u.SideQuestions++
			}
		case FeatureSideQuestionRejected:
			u.SideQuestionRejections++
		case FeatureSideQuestionFailed:
			u.SideQuestionFailures++
		case FeatureWaitRegistered:
			u.Waits++
			if event.All {
				u.AllWaits++
			}
			if event.Progress {
				u.ProgressWaits++
			}
		case FeatureProgressDelivered:
			u.ProgressYields++
		case FeatureAssignmentDelivered:
			if event.Sources {
				u.AssignmentSources++
			}
		}
		result[fact.Thread] = u
	}
	return result, nil
}

func featureUse(t *testing.T, service *Service, thread string) (FeatureUse, FeatureUse, []string) {
	t.Helper()
	snapshot := service.FeatureUse(t.Context(), thread)
	if snapshot.Err != nil {
		t.Fatal(snapshot.Err)
	}
	return snapshot.Self, snapshot.Descendants, snapshot.Threads
}

func seedFeatureUse(t *testing.T, service *Service, thread string, counts FeatureUse) {
	t.Helper()
	owner, err := service.owner(thread)
	if err != nil {
		t.Fatal(err)
	}
	source := (*owner.featureSource.Load()).(*featureRecorder)
	source.seed[thread] = counts
}
