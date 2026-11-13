package appserver

// FinalCaptureRequest is authenticated native completion authority, not a model tool input.
type FinalCaptureRequest struct {
	RootThreadID      string   `json:"rootThreadId"`
	ThreadID          string   `json:"threadId"`
	WorkID            string   `json:"workId"`
	RecipientThreadID string   `json:"recipientThreadId"`
	RecipientCwd      *string  `json:"recipientCwd"`
	Sources           []string `json:"sources"`
}

type FinalCaptureResponse struct {
	Capture *FrozenFinalCapture `json:"capture"`
}

type FrozenFinalCapture struct {
	ResourceRef string `json:"resourceRef"`
	FullText    string `json:"fullText"`
}

type FinalCaptureProjectRequest struct {
	RootThreadID       string          `json:"rootThreadId"`
	ThreadID           string          `json:"threadId"`
	WorkID             string          `json:"workId"`
	RecipientThreadID  string          `json:"recipientThreadId"`
	RecipientCwd       *string         `json:"recipientCwd"`
	RecipientContextID string          `json:"recipientContextId"`
	ResourceRef        string          `json:"resourceRef"`
	Seen               []CaptureCursor `json:"seen"`
}

type FinalCaptureProjectResponse struct {
	Text string `json:"text"`
}
