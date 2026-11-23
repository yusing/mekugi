// Package observation contains decoded, read-only session facts. These values
// carry no backend request IDs, process handles, or execution capabilities.
package observation

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
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

// ResponseProgress identifies provider response activity; callers retain turn admission.
// Background tool completions and non-model items do not prove a retrying request recovered.
func (e Event) ResponseProgress() bool {
	switch e.Kind {
	case AgentDelta, ReasoningDelta, ReasoningPartAdded:
		return true
	case ItemStarted:
		switch e.Item.Type {
		case "agentMessage", "reasoning", "webSearch", "fileChange", "imageView", "imageGeneration",
			"mcpToolCall", "collabAgentToolCall", "dynamicToolCall":
			return true
		case "commandExecution":
			return !e.Item.UserShell
		}
	case ItemCompleted:
		switch e.Item.Type {
		case "agentMessage", "reasoning":
			return true
		}
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
	AgentStart                                         *AgentStart
	SideAnswer                                         *SideAnswer
	SourceAcquisition                                  *SourceAcquisition
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

// AgentStart records the accepted child identity, resolved settings and any plaintext assignment.
type AgentStart struct{ AgentPath, Assignment, Model, ReasoningEffort string }

type SideAnswer struct{ Target, Question, Answer string }

type CommandAction struct{ Type, Command, Path, Query string }
type WebSearchAction struct {
	Type, Query, URL, Pattern string
	Queries                   []string
}

type Question struct {
	ID       string           `json:"id"`
	Header   string           `json:"header"`
	Question string           `json:"question"`
	IsSecret bool             `json:"isSecret"`
	Options  []QuestionOption `json:"options"`
}

func (q *Question) UnmarshalJSON(data []byte) error {
	var stored struct {
		ID       string           `json:"id"`
		Header   string           `json:"header"`
		Question string           `json:"question"`
		Title    string           `json:"title"`
		IsSecret bool             `json:"isSecret"`
		Options  []jsontext.Value `json:"options"`
	}
	if err := json.Unmarshal(data, &stored, json.MatchCaseInsensitiveNames(true)); err != nil {
		return err
	}
	decoded := Question{ID: stored.ID, Header: stored.Header, Question: stored.Question, IsSecret: stored.IsSecret}
	if decoded.Question == "" {
		decoded.Question = stored.Title
	}
	if stored.Options != nil {
		decoded.Options = make([]QuestionOption, 0, len(stored.Options))
	}
	for _, value := range stored.Options {
		var option QuestionOption
		if value.Kind() == '"' {
			if err := json.Unmarshal(value, &option.Label); err != nil {
				return err
			}
		} else if err := json.Unmarshal(value, &option); err != nil {
			return err
		}
		decoded.Options = append(decoded.Options, option)
	}
	*q = decoded
	return nil
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitzero"`
	Preview     string `json:"preview,omitzero"`
	Recommended bool   `json:"recommended,omitzero"`
}
type FileChange struct {
	Path, Diff string
	Kind       struct{ Type, MovePath string }
}

// Source is one decoded rollout, before timing synthesis or retained-record joins.
type Source struct {
	Thread, Cwd string
	AgentPath   string // Empty when the native canonical identity is unavailable.
	Events      []Event
	Unsupported map[string]int
	Bytes       int
}
