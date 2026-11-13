package sessionpresentation

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/observation"
)

// FeatureReader borrows detached counts from the native launch measurement owner.
type FeatureReader interface {
	ReadFeatureUse(context.Context, []string) (map[string]observation.FeatureUse, error)
}

// FeatureSnapshot joins a native measurement with the admitted descendant set.
// Err means neither column is a complete measurement, not that it measured zero.
type FeatureSnapshot struct {
	Self, Descendants observation.FeatureUse
	Threads           []string
	Err               error
}

// ReadNativeFeatureUse aggregates a detached native family selection without
// admitting, resuming, or retaining logical work in Go.
func ReadNativeFeatureUse(ctx context.Context, thread string, descendants []string, source FeatureReader) (result FeatureSnapshot) {
	result.Threads = append([]string(nil), descendants...)
	if source == nil {
		result.Err = errors.New("native feature measurement is unavailable")
		return
	}
	ids := append([]string{thread}, result.Threads...)
	values, err := source.ReadFeatureUse(ctx, ids)
	result.Err = err
	if err != nil {
		return
	}
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			result.Err = errors.New("native feature measurement is incomplete")
			return
		}
	}
	result.Self = values[thread]
	for _, id := range result.Threads {
		addFeatureUse(&result.Descendants, values[id])
	}
	return
}

func addFeatureUse(u *observation.FeatureUse, other observation.FeatureUse) {
	u.Compactions += other.Compactions
	u.SelfCompactions += other.SelfCompactions
	u.CompactionPreloads += other.CompactionPreloads
	u.Spawns += other.Spawns
	u.SpawnPreloads += other.SpawnPreloads
	u.PreloadRejections += other.PreloadRejections
	u.Finals += other.Finals
	u.CompletionChanges += other.CompletionChanges
	u.Messages += other.Messages
	u.SideQuestions += other.SideQuestions
	u.SideQuestionRejections += other.SideQuestionRejections
	u.SideQuestionFailures += other.SideQuestionFailures
	u.Waits += other.Waits
	u.AllWaits += other.AllWaits
	u.ProgressWaits += other.ProgressWaits
	u.ProgressYields += other.ProgressYields
	u.AssignmentSources += other.AssignmentSources
	u.MessageSources += other.MessageSources
	u.SelfCompactionsCompleted += other.SelfCompactionsCompleted
	u.SelfCompactionFailures += other.SelfCompactionFailures
}
