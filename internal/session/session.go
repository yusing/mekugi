// Package session defines presentation values and user intent across native runtimes.
// It owns no runtime processes, tools, permissions, or durable evidence.
package session

import "context"

type Event struct {
	Commands   []string
	Kind       string
	ID         string
	Role       string
	Text       string
	SessionID  string
	Model      string
	Failed     bool
	Prompt     *Prompt
	Edit       *Edit
	Historical bool
	Caller     string
	Models     []Model
	Settings   *Settings
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
