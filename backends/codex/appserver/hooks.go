package appserver

// Source: codex-rs/app-server-protocol/src/protocol/v2/hook.rs HookRunSummary.
type HookRun struct {
	ID            string `json:"id"`
	EventName     string `json:"eventName"`
	HandlerType   string `json:"handlerType"`
	ExecutionMode string `json:"executionMode"`
	SourcePath    string `json:"sourcePath"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage"`
	StartedAt     int64  `json:"startedAt"` // Host epoch seconds, not milliseconds.
	CompletedAt   *int64 `json:"completedAt"`
	DurationMS    *int64 `json:"durationMs"`
	Entries       []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}
