package binding

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/observation"
)

// Source: apps/mekugi/internal/router/app_server_events.go:67:122@1bcbdd5a.
type TokenUsage struct {
	TotalTokens           uint64 `json:"totalTokens"`
	InputTokens           uint64 `json:"inputTokens"`
	OutputTokens          uint64 `json:"outputTokens"`
	CachedInputTokens     uint64 `json:"cachedInputTokens"`
	ReasoningOutputTokens uint64 `json:"reasoningOutputTokens"`
}

type Turn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Error  *TurnError `json:"error"`
}

type TurnError struct {
	Message        string         `json:"message"`
	CodexErrorInfo jsontext.Value `json:"codexErrorInfo"`
}

// Recoverable identifies transient host failures without interpreting prose.
func (e *TurnError) Recoverable() bool {
	var code string
	if e.CodexErrorInfo.Kind() == '"' {
		_ = json.Unmarshal(e.CodexErrorInfo, &code)
	} else if e.CodexErrorInfo.Kind() == '{' {
		var variants map[string]struct {
			HTTPStatusCode *int `json:"httpStatusCode"`
		}
		if json.Unmarshal(e.CodexErrorInfo, &variants) != nil || len(variants) != 1 {
			return false
		}
		for name, details := range variants {
			if status := details.HTTPStatusCode; status != nil && *status >= 400 && *status < 500 && *status != 408 {
				return false
			}
			code = name
		}
	}
	switch code {
	case "serverOverloaded", "internalServerError", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts":
		return true
	}
	return false
}

type Notification struct {
	Command *CommandObservation `json:"-"`
	Status  struct {
		Type string `json:"type"`
	} `json:"status"`
	ThreadID   string             `json:"threadId"`
	TurnID     string             `json:"turnId"`
	ItemID     string             `json:"itemId"`
	Delta      string             `json:"delta"`
	ProcessID  string             `json:"processId"`
	Stdin      string             `json:"stdin"`
	Item       ThreadItem         `json:"item"`
	Run        *appserver.HookRun `json:"run,omitempty"`
	Thread     ThreadInfo         `json:"thread"`
	TokenUsage struct {
		Total              TokenUsage  `json:"total"`
		Last               *TokenUsage `json:"last"`
		ModelContextWindow uint64      `json:"modelContextWindow"`
	} `json:"tokenUsage"`
	Turn Turn `json:"turn"`
}

func (p Notification) Observation(method string, at time.Time) observation.Event {
	e := observation.Event{At: at, Kind: observation.Kind(method), ThreadID: p.ThreadID, TurnID: p.TurnID,
		ItemID: cmp.Or(p.ItemID, p.Item.ID), Delta: p.Delta, Item: p.Item.Observation(),
		ThreadStatus: p.Status.Type, ProcessID: p.ProcessID, Stdin: p.Stdin,
		TurnStatus: p.Turn.Status, Context: observation.Context{Window: p.TokenUsage.ModelContextWindow},
	}
	if p.Thread.ID != "" {
		e.Thread = p.Thread.Observation()
	}
	if p.TokenUsage.Last != nil {
		e.Context.Tokens, e.Context.Known = p.TokenUsage.Last.TotalTokens, true
	}
	if method == "turn/started" || method == "turn/completed" {
		e.TurnID = p.Turn.ID
	}
	return e
}

// DecodeNotification preserves native payloads while sharing command normalization
// with pre-queue evidence consumers. It never dispatches or changes the input.
// Source: apps/mekugi/internal/router/app_server_ui.go:856:866@1bcbdd5a.
func DecodeNotification(method string, raw jsontext.Value) (Notification, error) {
	var p Notification
	if err := json.Unmarshal(raw, &p); err != nil {
		return Notification{}, err
	}
	if command, ok := DecodeCommandObservation(method, raw); ok {
		p.Command = &command
		p.Delta = command.Delta
		if p.Item.Type == "commandExecution" {
			p.Item.Command, p.Item.AggregatedOutput, p.Item.ExitCode = command.Command, command.Output, command.Exit
		}
	}
	return p, nil
}
