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
	FeatureWaitProgress
	FeatureProgressYield
	FeatureAssignmentSources
	FeatureMessageSources
	FeatureSelfCompactionCompleted
	FeatureCompactionCostSkip
	FeatureCompactionPreflightSkip
	FeatureSelfCompactionFailure
)

// FeatureUse counts uses in this launch. Totals precede the subsets they
// contain. Counts are invocation-local: checkpoints, resume and fork do not
// restore them.
type FeatureUse struct {
	Compactions        uint64 // Completed native context resets, whoever initiated them.
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
	SideQuestionRejections, SideQuestionFailures  uint64
	Waits                                         uint64 // Newly registered wait_agent waits.
	AllWaits                                      uint64
	ProgressWaits, ProgressYields                 uint64
	AssignmentSources, MessageSources             uint64 // Accepted source-bearing sends, including read diagnostics.
	SelfCompactionsCompleted                      uint64
	CompactionCostSkips, CompactionPreflightSkips uint64
	SelfCompactionFailures                        uint64
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
	case FeatureWaitProgress:
		return &u.ProgressWaits
	case FeatureProgressYield:
		return &u.ProgressYields
	case FeatureAssignmentSources:
		return &u.AssignmentSources
	case FeatureMessageSources:
		return &u.MessageSources
	case FeatureSelfCompactionCompleted:
		return &u.SelfCompactionsCompleted
	case FeatureCompactionCostSkip:
		return &u.CompactionCostSkips
	case FeatureCompactionPreflightSkip:
		return &u.CompactionPreflightSkips
	case FeatureSelfCompactionFailure:
		return &u.SelfCompactionFailures
	}
	panic("unknown feature")
}

func (u *FeatureUse) add(other FeatureUse) {
	for feature := FeatureCompaction; feature <= FeatureSelfCompactionFailure; feature++ {
		*u.counter(feature) += *other.counter(feature)
	}
}

// ObserveFeature records a use for the thread that used it. The caller owns
// once-only registration or confirmation.
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

// ObserveMessageFeatures attributes accepted foreign sends and independent btw
// outcomes to their sender. Unlike local effects, retries cross owner boundaries,
// so these launch-local receipts deduplicate each operation's counted facts.
func (s *Service) ObserveMessageFeatures(thread, call string, features ...Feature) {
	if thread == "" || call == "" {
		return
	}
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	if s.messageFeatures == nil {
		s.messageFeatures = make(map[[2]string]uint64)
	}
	key := [2]string{thread, call}
	seen, use := s.messageFeatures[key], s.features[thread]
	for _, feature := range features {
		bit := uint64(1) << feature
		if seen&bit == 0 {
			*use.counter(feature)++
			seen |= bit
		}
	}
	s.messageFeatures[key], s.features[thread] = seen, use
}

// ObserveCompaction counts a live native boundary once. Restored history is not
// current-launch feature use.
func (s *Service) ObserveCompaction(thread, item string) {
	if thread == "" || item == "" {
		return
	}
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	if s.compactions == nil {
		s.compactions = make(map[[2]string]bool)
	}
	key := [2]string{thread, item}
	if s.compactions[key] {
		return
	}
	s.compactions[key] = true
	use := s.features[thread]
	use.Compactions++
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
func confirmedFeatures(step string, effect Effect) (features []Feature) {
	if effect.Sources != "" && (step == "admit" || step == "followup") {
		features = append(features, FeatureAssignmentSources)
	}
	switch {
	case step == "compact":
		return []Feature{FeatureSelfCompactionCompleted}
	case step == "admit":
		if sources, _ := compact.ReloadSources(effect.Child.Preload.Selection); len(sources) > 0 {
			return append(features, FeatureSpawn, FeatureSpawnPreload)
		}
		return append(features, FeatureSpawn)
	case step == "final" && effect.Outcome == Completed:
		_, selection, found := compact.SplitCompactReload(effect.Message)
		if sources, _ := compact.Sources(selection); found && len(sources) > 0 && compact.CompletionSources(selection) == nil {
			return []Feature{FeatureFinal, FeatureCompletionChanges}
		}
		return []Feature{FeatureFinal}
	}
	return features
}
