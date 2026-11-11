package codexbinding

import (
	"context"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"sync"
	"testing"
)

// featureRecorder is the native measurement stand-in for existing producer fixtures.
// It reduces accepted facts only; production Go neither keeps this state nor increments counts.
type featureRecorder struct {
	mu    sync.Mutex
	facts map[[3]string]control.FeatureFact
	seed  map[string]control.FeatureUse
}

func newFeatureRecorder() *featureRecorder {
	return &featureRecorder{facts: make(map[[3]string]control.FeatureFact), seed: make(map[string]control.FeatureUse)}
}

func (r *featureRecorder) RecordFeature(_ context.Context, fact control.FeatureFact) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [3]string{fact.Thread, fact.Operation, string(fact.Event.Kind)}
	if _, seen := r.facts[key]; !seen {
		r.facts[key] = fact
	}
}

func (r *featureRecorder) InvalidateFeatureUse(error) {}

func (r *featureRecorder) ReadFeatureUse(_ context.Context, threads []string) (map[string]control.FeatureUse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]control.FeatureUse)
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
		case control.FeatureResetCompleted:
			u.Compactions++
		case control.FeatureSelfResetRequested:
			u.SelfCompactions++
			if event.Preload {
				u.CompactionPreloads++
			}
		case control.FeatureSelfResetCompleted:
			u.SelfCompactionsCompleted++
		case control.FeatureSelfResetFailed:
			u.SelfCompactionFailures++
		case control.FeatureChildAdmitted:
			u.Spawns++
			if event.Preload {
				u.SpawnPreloads++
			}
			if event.Sources {
				u.AssignmentSources++
			}
		case control.FeaturePreloadRejected:
			u.PreloadRejections++
		case control.FeatureFinalDelivered:
			u.Finals++
			if event.Changes {
				u.CompletionChanges++
			}
		case control.FeatureMessageAccepted:
			u.Messages++
			if event.Sources {
				u.MessageSources++
			}
			if event.BTW {
				u.SideQuestions++
			}
		case control.FeatureSideQuestionRejected:
			u.SideQuestionRejections++
		case control.FeatureSideQuestionFailed:
			u.SideQuestionFailures++
		case control.FeatureWaitRegistered:
			u.Waits++
			if event.All {
				u.AllWaits++
			}
			if event.Progress {
				u.ProgressWaits++
			}
		case control.FeatureProgressDelivered:
			u.ProgressYields++
		case control.FeatureAssignmentDelivered:
			if event.Sources {
				u.AssignmentSources++
			}
		}
		result[fact.Thread] = u
	}
	return result, nil
}

func featureUse(t *testing.T, service *control.Service, thread string) (control.FeatureUse, control.FeatureUse, []string) {
	t.Helper()
	snapshot := service.FeatureUse(t.Context(), thread)
	if snapshot.Err != nil {
		t.Fatal(snapshot.Err)
	}
	return snapshot.Self, snapshot.Descendants, snapshot.Threads
}

func bindFeatureRecorder(service *control.Service, thread string, driver *Driver) {
	source := newFeatureRecorder()
	service.BindFeatureUse(thread, source)
	driver.BindFeatures(source)
}
