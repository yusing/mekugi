package appserver

// ChildHostAdmissionRequest carries native-reserved identity and resolved cwd
// from the owning connection before child startup, not model-supplied arguments.
type ChildHostAdmissionRequest struct {
	RootThreadID   string  `json:"rootThreadId"`
	ParentThreadID string  `json:"parentThreadId"`
	ThreadID       string  `json:"threadId"`
	LocalCwd       *string `json:"localCwd"`
}

type ChildHostAdmissionResponse struct {
	ThreadID string `json:"threadId"`
}
