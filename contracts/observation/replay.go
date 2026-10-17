// Package observation contains decoded, read-only session facts. These values
// carry no backend request IDs, process handles, or execution capabilities.
package observation

import (
	"encoding/json/jsontext"
	"time"
)

type Kind string

const (
	TurnStarted       Kind = "turn/started"
	TurnCompleted     Kind = "turn/completed"
	ItemStarted       Kind = "item/started"
	ItemCompleted     Kind = "item/completed"
	AgentDelta        Kind = "item/agentMessage/delta"
	ReasoningDelta    Kind = "item/reasoning/summaryTextDelta"
	CommandDelta      Kind = "item/commandExecution/outputDelta"
	ProviderStarted   Kind = "replay/providerStarted"
	ProviderCompleted Kind = "replay/providerCompleted"
)

// Event is an immutable observation. Started is present on retained item
// completions; synthetic starts and deltas are the replay consumer's policy.
type Event struct {
	At, Started                                 time.Time
	Kind                                        Kind
	ThreadID, TurnID, ItemID, Delta, TurnStatus string
	Item                                        Item
}

// Item contains the retained facts used by session presentation. Content and
// question options preserve opaque user-content blocks for their own consumer;
// raw/private reasoning is never included.
type Item struct {
	ID, Type, Status, Text, Delivery, Phase, Cwd, Kind, Path string
	Command, ProcessID, ClientID, AgentThreadID, AgentPath   string
	Tool, SenderThreadID                                     string
	Prompt, Model, ReasoningEffort                           string
	ReceiverThreadIDs                                        []string
	DurationMS                                               *int64
	Summary                                                  []string
	Content                                                  jsontext.Value
	AggregatedOutput                                         *string
	ExitCode                                                 *int
	AgentsStates                                             map[string]AgentState
	Questions                                                []Question
	Changes                                                  []FileChange
}

type AgentState struct{ Status string }
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
