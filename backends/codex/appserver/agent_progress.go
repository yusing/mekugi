package appserver

type CaptureCursor struct {
	Key          string `json:"key"`
	ThroughChunk uint64 `json:"throughChunk"`
}

type CaptureInterval struct {
	Key             string  `json:"key"`
	ChangeID        string  `json:"changeId"`
	RecordOrder     uint64  `json:"recordOrder"`
	LegacyRecordKey *string `json:"legacyRecordKey"`
	FirstChunk      uint64  `json:"firstChunk"`
	LastChunk       uint64  `json:"lastChunk"`
}

type CaptureCoverage struct {
	Status      string   `json:"status"`
	Diagnostics []string `json:"diagnostics"`
}

type ProgressActor struct {
	ThreadID string `json:"threadId"`
	WorkID   string `json:"workId"`
	TaskName string `json:"taskName"`
}

type FrozenAgentCapture struct {
	Actor     ProgressActor     `json:"actor"`
	Intervals []CaptureInterval `json:"intervals"`
}

// SummaryResult is the native auxiliary sampler's tagged result, not primary input.
type SummaryResult struct {
	Status string `json:"status"`
	Text   string `json:"text,omitzero"`
	Reason string `json:"reason,omitzero"`
}

type PreparedProgressAgent struct {
	Actor     ProgressActor     `json:"actor"`
	Intervals []CaptureInterval `json:"intervals"`
	Summary   SummaryResult     `json:"summary"`
}

type ProgressFreezeRequest struct {
	RootThreadID       string          `json:"rootThreadId"`
	CallerThreadID     string          `json:"callerThreadId"`
	CallerWorkID       string          `json:"callerWorkId"`
	WaitID             string          `json:"waitId"`
	BatchID            string          `json:"batchId"`
	RecipientContextID string          `json:"recipientContextId"`
	Selected           []ProgressActor `json:"selected"`
	Seen               []CaptureCursor `json:"seen"`
}

type ProgressFreezeResponse struct {
	SnapshotRef         string               `json:"snapshotRef"`
	PublicationRevision uint64               `json:"publicationRevision"`
	Agents              []FrozenAgentCapture `json:"agents"`
	Coverage            CaptureCoverage      `json:"coverage"`
}

type ProgressOutputRequest struct {
	RootThreadID       string                  `json:"rootThreadId"`
	CallerThreadID     string                  `json:"callerThreadId"`
	CallerWorkID       string                  `json:"callerWorkId"`
	WaitID             string                  `json:"waitId"`
	BatchID            string                  `json:"batchId"`
	RecipientContextID string                  `json:"recipientContextId"`
	SnapshotRef        string                  `json:"snapshotRef"`
	Agents             []PreparedProgressAgent `json:"agents"`
	Coverage           CaptureCoverage         `json:"coverage"`
}

type ProgressOutputResponse struct {
	Text               string            `json:"text"`
	RecoveryRef        *string           `json:"recoveryRef"`
	DeliveredIntervals []CaptureInterval `json:"deliveredIntervals"`
}
