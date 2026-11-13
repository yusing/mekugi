// Package observation contains decoded, read-only session facts. These values
// carry no backend request IDs, process handles, or execution capabilities.
package observation

import (
	"encoding/json/jsontext"
	"time"
)

type Kind string

const (
	TurnStarted               Kind = "turn/started"
	TurnCompleted             Kind = "turn/completed"
	ItemStarted               Kind = "item/started"
	ItemCompleted             Kind = "item/completed"
	AgentDelta                Kind = "item/agentMessage/delta"
	ReasoningDelta            Kind = "item/reasoning/summaryTextDelta"
	ReasoningPartAdded        Kind = "item/reasoning/summaryPartAdded"
	CommandDelta              Kind = "item/commandExecution/outputDelta"
	ProviderStarted           Kind = "replay/providerStarted"
	ProviderCompleted         Kind = "replay/providerCompleted"
	CoordinationObserved      Kind = "agent/coordination"
	ContextResetObserved      Kind = "thread/contextReset"
	SourceAcquisitionObserved Kind = "thread/sourceAcquisition"
)

// ResponseProgress identifies response activity; callers retain turn admission.
func (k Kind) ResponseProgress() bool {
	switch k {
	case ItemStarted, ItemCompleted, AgentDelta, ReasoningDelta, ReasoningPartAdded:
		return true
	}
	return false
}

// Event is an immutable observation. Started is present on retained item
// completions; synthetic starts and deltas are the replay consumer's policy.
type Event struct {
	At, Started                                 time.Time
	Kind                                        Kind
	ThreadID, TurnID, ItemID, Delta, TurnStatus string
	Item                                        Item
	Thread                                      Thread
	ThreadStatus, ProcessID, Stdin              string
	Context                                     Context
	Retry                                       string // Live retry status, absent for terminal errors.
	Diagnostic                                  string // Non-retrying provider error, not a turn completion.
	// Native facts survive live and retained delivery without becoming tool
	// outputs or execution authority. Inherited preserves recorded attribution.
	Coordination      *CoordinationProjection
	NativeMessage     *NativeMessage
	Reset             *ContextResetEvent
	SourceAcquisition *SourceAcquisition
	Inherited         bool
}

// Thread is admitted metadata, not a filesystem or host-request capability.
type Thread struct {
	ID, Nickname, Path, Role string
	NonAttachable            bool
}

type Context struct {
	Tokens, Window uint64
	Known          bool
}

// CompactionMethod is supplied by the initiating runtime, never inferred from
// summary text. The zero value preserves historical summary presentation.
type CompactionMethod string

const (
	ContextReset   CompactionMethod = "reset"
	ContextSummary CompactionMethod = "summary"
)

// Item contains the retained facts used by session presentation. Content and
// question options preserve opaque user-content blocks for their own consumer;
// raw/private reasoning is never included.
type Item struct {
	CompactionMethod                                   CompactionMethod
	CompactionContext                                  *Context
	SavedPath                                          string
	CompactionReference                                string // Exact retained replacement bound to this native boundary.
	UserShell                                          bool   // Host-identified command entered by the user.
	ID, Type, Status, Text, Delivery, Phase, Cwd, Path string
	Command, ProcessID, ClientID                       string
	Tool                                               string
	ReceiverThreadIDs                                  []string
	TimeoutMS                                          *int64
	Server                                             string         // MCP server identity.
	Arguments, Result, Error                           jsontext.Value // Opaque MCP input and outcome.
	DurationMS                                         *int64
	Summary                                            []string
	Content                                            jsontext.Value
	AggregatedOutput                                   *string
	ExitCode                                           *int
	Questions                                          []Question
	Changes                                            []FileChange
	CommandActions                                     []CommandAction
	Query                                              string
	Action                                             *WebSearchAction
	Results                                            *int
}

type CommandAction struct{ Type, Command, Path, Query string }
type WebSearchAction struct {
	Type, Query, URL, Pattern string
	Queries                   []string
}

type Question struct {
	ID, Header, Question, Title string
	IsSecret                    bool
	Options                     jsontext.Value
}
type FileChange struct {
	Path, Diff string
	Kind       struct{ Type, MovePath string }
}

// Source is one decoded rollout, before timing synthesis or retained-record joins.
type Source struct {
	Thread, Cwd string
	Events      []Event
	Unsupported map[string]int
	Bytes       int
}
