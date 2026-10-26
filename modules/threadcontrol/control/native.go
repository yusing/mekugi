package control

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
)

// Native is the server's semantic thread API. The execution owner chooses host
// requests and confirms their results; collaboration never dispatches wire RPCs.
type Native interface {
	Runtime(string) (Runtime, error)
	Resume(context.Context, string) (Runtime, error)
	Confirm(context.Context, NativeInput) error
	Create(context.Context, string, string, Settings) (Receipt, error)
}

// Runtime is one admitted native execution owner's typed operations.
type Runtime interface {
	Thread(context.Context, string) (ThreadInfo, error)
	Inspect(context.Context, string) (SessionDetails, error)
	Configuration(context.Context, string, string) (jsontext.Value, error)
	Input(context.Context, NativeInput) (string, error)
	Interrupt(context.Context, string, string) error
	Confirm(context.Context, NativeInput) error
}

// SessionMetrics are current runtime observations, not cumulative provider usage.
type SessionMetrics struct {
	ContextTokens *uint64 `json:"context_tokens"`
	ContextWindow *uint64 `json:"context_window"`
	Compactions   *uint64 `json:"compactions"`
}

type SessionDetails struct {
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	ServiceTier     *string `json:"service_tier"`
	SessionMetrics
}

type ThreadInfo struct {
	Thread   string  `json:"id"`
	Session  string  `json:"sessionId"`
	Path     string  `json:"path"`
	Provider string  `json:"modelProvider"`
	Parent   *string `json:"parentThreadId"`
	Fork     *string `json:"forkedFromId"`
}

// NativeInput preserves agent attribution separately from human composer input.
// Run starts or steers work; a retained-only message leaves idle work idle.
type NativeInput struct {
	Thread, ParentTurn, TargetTurn, ID, Sender, Recipient, Kind, Message string
	Run                                                                  bool
	Settings                                                             *Settings
	Wake                                                                 *WaitResult
}

// RetainedWaitOutput binds host confirmation to this exact dispatch, including
// its frozen metrics. Provider projection removes the host-only identity.
func (input NativeInput) RetainedWaitOutput() string {
	raw, _ := json.Marshal(struct {
		ID string `json:"id"`
		*WaitResult
	}{input.ID, input.Wake}, json.Deterministic(true))
	return string(raw)
}

// MessageItemID is the retained host item identity for attributed delivery.
func MessageItemID(id string) string {
	return fmt.Sprintf("threadcontrol_%x", sha256.Sum256([]byte(id)))
}
