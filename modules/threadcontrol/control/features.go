package control

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

// FeatureEvent names a committed domain fact, not a tool success or a counter delta.
// The native app-server owns reduction and once-per-fact receipts.
type FeatureKind string

const (
	FeatureResetCompleted       FeatureKind = "resetCompleted"
	FeatureSelfResetRequested   FeatureKind = "selfResetRequested"
	FeatureSelfResetCompleted   FeatureKind = "selfResetCompleted"
	FeatureSelfResetFailed      FeatureKind = "selfResetFailed"
	FeatureChildAdmitted        FeatureKind = "childAdmitted"
	FeaturePreloadRejected      FeatureKind = "preloadRejected"
	FeatureFinalDelivered       FeatureKind = "finalDelivered"
	FeatureMessageAccepted      FeatureKind = "messageAccepted"
	FeatureSideQuestionRejected FeatureKind = "sideQuestionRejected"
	FeatureSideQuestionFailed   FeatureKind = "sideQuestionFailed"
	FeatureWaitRegistered       FeatureKind = "waitRegistered"
	FeatureProgressDelivered    FeatureKind = "progressDelivered"
	FeatureAssignmentDelivered  FeatureKind = "assignmentDelivered"
)

type FeatureEvent struct {
	Kind     FeatureKind `json:"type"`
	Preload  bool        `json:"preload,omitzero"`
	Sources  bool        `json:"sources,omitzero"`
	Changes  bool        `json:"changes,omitzero"`
	BTW      bool        `json:"btw,omitzero"`
	All      bool        `json:"all,omitzero"`
	Progress bool        `json:"progress,omitzero"`
}

type FeatureFact struct {
	Thread    string       `json:"threadId"`
	Operation string       `json:"operation"`
	Event     FeatureEvent `json:"event"`
}

// FeatureSource is the root launch's native measurement owner. Record failures
// make subsequent reads unavailable; they never roll back accepted domain work.
type FeatureSource interface {
	RecordFeature(context.Context, FeatureFact)
	InvalidateFeatureUse(error)
	ReadFeatureUse(context.Context, []string) (map[string]FeatureUse, error)
}

// FeatureUse is a detached native snapshot. It is never checkpointed or incremented in Go.
type FeatureUse struct {
	Compactions              uint64 `json:"compactions"`
	SelfCompactions          uint64 `json:"selfCompactions"`
	CompactionPreloads       uint64 `json:"compactionPreloads"`
	Spawns                   uint64 `json:"spawns"`
	SpawnPreloads            uint64 `json:"spawnPreloads"`
	PreloadRejections        uint64 `json:"preloadRejections"`
	Finals                   uint64 `json:"finals"`
	CompletionChanges        uint64 `json:"completionChanges"`
	Messages                 uint64 `json:"messages"`
	SideQuestions            uint64 `json:"sideQuestions"`
	SideQuestionRejections   uint64 `json:"sideQuestionRejections"`
	SideQuestionFailures     uint64 `json:"sideQuestionFailures"`
	Waits                    uint64 `json:"waits"`
	AllWaits                 uint64 `json:"allWaits"`
	ProgressWaits            uint64 `json:"progressWaits"`
	ProgressYields           uint64 `json:"progressYields"`
	AssignmentSources        uint64 `json:"assignmentSources"`
	MessageSources           uint64 `json:"messageSources"`
	SelfCompactionsCompleted uint64 `json:"selfCompactionsCompleted"`
	SelfCompactionFailures   uint64 `json:"selfCompactionFailures"`
}

func (u *FeatureUse) add(other FeatureUse) {
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

// FeatureSnapshot joins a native measurement with the admitted descendant set.
// Err means neither column is a complete measurement, not that it measured zero.
type FeatureSnapshot struct {
	Self, Descendants FeatureUse
	Threads           []string
	Err               error
}

// BindFeatureUse borrows one source for the tree. A fresh root invocation replaces
// only this reference; native counters do not survive that invocation.
func (s *Service) BindFeatureUse(thread string, source FeatureSource) {
	if owner, err := s.owner(thread); err == nil {
		owner.featureSource.Store(&source)
	}
}

func (s *Service) RecordFeature(ctx context.Context, thread, operation string, event FeatureEvent) {
	if owner, err := s.owner(thread); err == nil {
		if source := owner.featureSource.Load(); source != nil && *source != nil {
			(*source).RecordFeature(ctx, FeatureFact{Thread: thread, Operation: operation, Event: event})
		}
	}
}

func (s *Service) FeatureUse(ctx context.Context, thread string) FeatureSnapshot {
	var source FeatureSource
	if owner, err := s.owner(thread); err == nil {
		if bound := owner.featureSource.Load(); bound != nil {
			source = *bound
		}
	}
	return s.ReadFeatureUse(ctx, thread, source)
}

// ReadFeatureUse also permits a native displayed thread before logical work has
// admitted its tree. Reading never starts or resumes a runtime.
func (s *Service) ReadFeatureUse(ctx context.Context, thread string, source FeatureSource) (result FeatureSnapshot) {
	for _, member := range s.Descendants(thread) {
		result.Threads = append(result.Threads, member.Thread)
	}
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
		result.Descendants.add(values[id])
	}
	return
}

func confirmedFeature(step string, effect Effect) *FeatureEvent {
	switch {
	case step == "compact":
		return &FeatureEvent{Kind: FeatureSelfResetCompleted}
	case step == "admit":
		sources, _ := compact.ReloadSources(effect.Child.Preload.Selection)
		return &FeatureEvent{Kind: FeatureChildAdmitted, Preload: len(sources) > 0, Sources: effect.Sources != ""}
	case step == "followup" && effect.Sources != "":
		return &FeatureEvent{Kind: FeatureAssignmentDelivered, Sources: true}
	case step == "final" && effect.Outcome == Completed:
		selection := CompletionSelection(effect.Message)
		return &FeatureEvent{Kind: FeatureFinalDelivered, Changes: selection != "" && compact.CompletionSources(selection) == nil}
	}
	return nil
}
