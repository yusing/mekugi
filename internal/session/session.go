// Package session defines presentation values and user intent across native runtimes.
// It owns no runtime processes, tools, permissions, or durable evidence.
package session

import "context"

type Event struct {
	CommandInfo  []Command
	Kind         string
	ID           string
	Role         string
	Text         string
	SessionID    string
	Cwd          string
	Model        string
	Failed       bool
	Prompt       *Prompt
	Edit         *Edit
	Historical   bool
	Caller       string
	Callers      []string
	Models       []Model
	Settings     *Settings
	Usage        *Usage
	Limit        *RateLimit
	Task         *Task
	Output       *CommandOutput
	Title        *SessionTitle
	Sessions     *SessionPage
	Change       *SessionChange
	SideID       string
	AgentMessage *AgentMessage
	Shell        *ShellResult
	CommandInput *CommandInput
	Skill        string // Canonical name from a successful native Skill receipt.
	SkillHistory *SkillHistory
}

// SkillHistory observes one saved current context independently of display.
// Messages cannot restore execution, permissions, or processes.
type SkillHistory struct {
	AgentID, Phase string
	Events         []Event
}

// CommandInput is received proposal text, not execution or completion evidence.
type CommandInput struct {
	Text     string
	Complete bool
}

type SavedSession struct {
	ID, Title, Cwd, Branch string
	Updated                int64
}
type SessionPage struct {
	ID, Cursor string
	Sessions   []SavedSession
}
type SessionListRequest struct {
	ID, Cwd, Cursor string
	Limit           int
}
type SessionListClient interface {
	ListSessions(context.Context, SessionListRequest) error
}

// An empty SessionID starts a new native session without inherited context.
type SessionChange struct{ ID, SessionID, Title, Cwd string }
type SessionChangeClient interface {
	ChangeSession(context.Context, SessionChange) error
}

type SessionTitle struct{ ID, SessionID, Title string }

type TitleClient interface {
	RenameSession(context.Context, SessionTitle) error
}

// CommandOutput is a native tail snapshot, not an append-only byte stream.
type CommandOutput struct {
	TaskID          string
	OutputFile      string // Native terminal aggregate, never a model-supplied operand.
	Reference       string // Presentation-owned immutable full-output continuation.
	Truncated, Done bool
}

// Usage totals are the latest native query-pipeline snapshot, including native
// subagents and prior retained turns. Never add successive cumulative snapshots.
type Usage struct {
	Models  map[string]ModelUsage `json:"modelUsage,omitempty"`
	CostUSD *float64              `json:"total_cost_usd,omitempty"`
}
type ModelUsage struct {
	Input         *uint64 `json:"inputTokens,omitempty"`
	Output        *uint64 `json:"outputTokens,omitempty"`
	Thinking      *uint64 `json:"thinkingTokens,omitempty"`
	CacheRead     *uint64 `json:"cacheReadInputTokens,omitempty"`
	CacheWrite    *uint64 `json:"cacheCreationInputTokens,omitempty"`
	ContextWindow *uint64 `json:"contextWindow,omitempty"`
}
type RateLimit struct {
	Status      string   `json:"status"`
	Window      string   `json:"rateLimitType,omitempty"`
	Utilization *float64 `json:"utilization,omitempty"`
	ResetsAt    *float64 `json:"resetsAt,omitempty"`
	Scope       string   `json:"limitScope,omitempty"`
}

// Task identity is not an agent principal or authorization to that agent's store.
type Task struct {
	ID, ToolID, Kind, Role, Description, Status, Summary string
	Ambient                                              bool
}
type TaskClient interface {
	StopTask(context.Context, string) error
}

// AgentID is checked by the native runtime against its own session metadata.
// A successful receipt confirms queue delivery, not child completion.
type AgentMessage struct{ ID, SessionID, AgentID, Text string }
type AgentMessageClient interface {
	SendAgentMessage(context.Context, AgentMessage) error
}

// Direct user-shell input is distinct from a model's permission-controlled tool.
// Native completion and transcript append completion settle it independently.
type ShellCommand struct{ ID, SessionID, Command string }
type ShellResult struct {
	ShellCommand
	Output   string
	ExitCode *int
	Retained bool
}
type ShellClient interface {
	RunShell(context.Context, ShellCommand) error
}

// Reset prepares a fresh native query. reset_ready is not durable completion:
// the first new input must establish native identity and emit reset.
type ResetClient interface {
	Reset(context.Context, string) error
}

type InputPart struct{ Text, ImagePath string }
type InputClient interface {
	SendInput(context.Context, []InputPart) error
}
type SideInput struct {
	ID, Source string
	Input      []InputPart
}
type SideClient interface {
	SendSide(context.Context, SideInput) error
	CloseSide(context.Context, string) error
}
type Command struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Arguments   string   `json:"argumentHint"`
	Aliases     []string `json:"aliases,omitempty"`
	Builtin     bool     `json:"builtin,omitzero"`
}

// Model choices are advertised by the runtime, not the inference router.
type Model struct {
	ID             string   `json:"value"`
	Resolved       string   `json:"resolvedModel,omitempty"`
	Name           string   `json:"displayName"`
	Description    string   `json:"description"`
	SupportsEffort bool     `json:"supportsEffort,omitzero"`
	Efforts        []string `json:"supportedEffortLevels,omitempty"`
}

// Settings carries invocation-local intent. An acknowledgement does not prove
// an effort's effective value: native policy may clamp it.
type Settings struct{ ID, Field, Value string }

type SettingsClient interface {
	SetSettings(context.Context, Settings) error
}

type Option struct{ Label, Description string }
type Question struct {
	Text, Header string
	Options      []Option
	Multiple     bool
}
type Prompt struct {
	ID, Tool, Description string
	ToolID, Caller        string // Exact native tool call and optional child identity.
	Questions             []Question
}
type Decision struct {
	ID      string
	Allow   bool
	Answers map[string]string
}

// Client preserves runtime semantics. A terminal result, not a successful Send,
// settles work; permission decisions never originate in the presentation layer.
type Client interface {
	Events() <-chan Event
	Send(context.Context, string) error
	Respond(context.Context, Decision) error
	Interrupt(context.Context) error
	Close() error
}

// Edit is a read-only proposed file operation, not evidence of execution.
// Partial content must never imply deletion of an unseen suffix.
type Edit struct {
	Path, Content, Old           string
	Replace, ReplaceAll, Partial bool
}
