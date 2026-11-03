package control

import "github.com/yusing/mekugi/modules/agentguidance/compact"

// Feature names one accepted model-facing feature use or one counted attempt.
type Feature uint8

const (
	FeatureCompaction Feature = iota
	FeatureSelfCompaction
	FeatureCompactionPreload
	FeatureSpawn
	FeatureSpawnPreload
	FeaturePreloadRejection
	FeatureFinal
	FeatureCompletionChanges
	FeatureMessage
	FeatureSideQuestion
	FeatureSideQuestionRejection
	FeatureSideQuestionFailure
	FeatureWait
	FeatureWaitAll
)

// FeatureUse counts uses in this launch. Totals precede the subsets they
// contain. Counts are invocation-local: checkpoints, resume and fork do not
// restore them.
type FeatureUse struct {
	Compactions        uint64 // Retained compaction summaries, whoever initiated them.
	SelfCompactions    uint64
	CompactionPreloads uint64
	Spawns             uint64 // Confirmed child admissions.
	SpawnPreloads      uint64
	PreloadRejections  uint64 // Spawn calls whose preload selection was rejected.
	Finals             uint64 // Confirmed completed final answers.
	CompletionChanges  uint64
	Messages           uint64 // Dispatched send_message calls, including btw.
	SideQuestions      uint64
	// btw calls rejected before dispatch, and dispatched ones whose answer
	// failed or whose target generation changed.
	SideQuestionRejections, SideQuestionFailures uint64
	Waits                                        uint64 // Newly registered wait_agent waits.
	AllWaits                                     uint64
}

func (u *FeatureUse) counter(feature Feature) *uint64 {
	switch feature {
	case FeatureCompaction:
		return &u.Compactions
	case FeatureSelfCompaction:
		return &u.SelfCompactions
	case FeatureCompactionPreload:
		return &u.CompactionPreloads
	case FeatureSpawn:
		return &u.Spawns
	case FeatureSpawnPreload:
		return &u.SpawnPreloads
	case FeaturePreloadRejection:
		return &u.PreloadRejections
	case FeatureFinal:
		return &u.Finals
	case FeatureCompletionChanges:
		return &u.CompletionChanges
	case FeatureMessage:
		return &u.Messages
	case FeatureSideQuestion:
		return &u.SideQuestions
	case FeatureSideQuestionRejection:
		return &u.SideQuestionRejections
	case FeatureSideQuestionFailure:
		return &u.SideQuestionFailures
	case FeatureWait:
		return &u.Waits
	case FeatureWaitAll:
		return &u.AllWaits
	}
	panic("unknown feature")
}

func (u *FeatureUse) add(other FeatureUse) {
	for feature := FeatureCompaction; feature <= FeatureWaitAll; feature++ {
		*u.counter(feature) += *other.counter(feature)
	}
}

// ObserveFeature records each feature once for the thread that used it.
func (s *Service) ObserveFeature(thread string, features ...Feature) {
	if thread == "" {
		return
	}
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	use := s.features[thread]
	for _, feature := range features {
		*use.counter(feature)++
	}
	s.features[thread] = use
}

// FeatureUse returns the thread's own counts and the sum over its admitted
// descendants, including settled ones, with the descendant thread identities.
func (s *Service) FeatureUse(thread string) (self, descendants FeatureUse, threads []string) {
	for _, member := range s.Descendants(thread) {
		threads = append(threads, member.Thread)
	}
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	for _, id := range threads {
		descendants.add(s.features[id])
	}
	return s.features[thread], descendants, threads
}

// confirmedFeatures classifies a confirmed effect step. Immediate and
// reconciled acknowledgements both confirm once, so each use counts once.
func confirmedFeatures(step string, effect Effect) []Feature {
	switch {
	case step == "admit":
		if sources, _ := compact.ReloadSources(effect.Child.Preload.Selection); len(sources) > 0 {
			return []Feature{FeatureSpawn, FeatureSpawnPreload}
		}
		return []Feature{FeatureSpawn}
	case step == "final" && effect.Outcome == Completed:
		_, selection, found := compact.SplitCompactReload(effect.Message)
		if sources, _ := compact.Sources(selection); found && len(sources) > 0 && compact.CompletionSources(selection) == nil {
			return []Feature{FeatureFinal, FeatureCompletionChanges}
		}
		return []Feature{FeatureFinal}
	}
	return nil
}
