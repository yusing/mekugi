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
	Thread                                      Thread
	ThreadStatus, ProcessID, Stdin              string
	Context                                     Context
}

// Thread is admitted metadata, not a filesystem or host-request capability.
type Thread struct{ ID, Nickname, Path, Role string }

type Context struct {
	Tokens, Window uint64
	Known          bool
}

// Item contains the retained facts used by session presentation. Content and
// question options preserve opaque user-content blocks for their own consumer;
// raw/private reasoning is never included.
type Item struct {
	SavedPath                                          string
	CompactionReference                                string // Exact retained replacement bound to this native boundary.
	UserShell                                          bool   // Host-identified command entered by the user.
	ID, Type, Status, Text, Delivery, Phase, Cwd, Path string
	Command, ProcessID, ClientID                       string
	Tool                                               string
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
