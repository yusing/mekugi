package observation

// FeatureUse is a detached native snapshot, not a Go counter or retained ledger.
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
