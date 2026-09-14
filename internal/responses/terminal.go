package responses

import (
	"encoding/json"
	"strings"
)

// TerminalState is validated response evidence. It is not request success.
type TerminalState uint8

const (
	TerminalUnknown TerminalState = iota
	TerminalInvalid
	TerminalPending
	TerminalCompleted
	TerminalSteered
	TerminalFailed
)

func (s TerminalState) String() string {
	switch s {
	case TerminalInvalid:
		return "invalid"
	case TerminalPending:
		return "pending"
	case TerminalCompleted:
		return "completed"
	case TerminalSteered:
		return "steered"
	case TerminalFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func (s TerminalState) Terminal() bool {
	return s == TerminalCompleted || s == TerminalSteered || s == TerminalFailed
}

// Status returns the response status named by an event. Callers must establish
// that this is a response event; unknown suffixes remain unknown wire values.
func (k Kind) Status() string { return strings.TrimPrefix(string(k), "response.") }

// Source: internal/router/client.go:826:899. JSON status and SSE event kind have
// different authority. A steered SSE terminal is accepted, but not successful.
// Keep malformed nested incomplete details non-steered, as at the original owner.
func ObserveTerminal(body []byte, stream bool) TerminalState {
	if stream {
		payload := strings.TrimSpace(string(body))
		if payload == "" || payload == "[DONE]" {
			return TerminalUnknown
		}
	}
	var envelope struct {
		Type   Kind   `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		if stream {
			return TerminalInvalid
		}
		return TerminalUnknown
	}
	status := envelope.Status
	if stream {
		if envelope.Type.Ancillary() {
			return TerminalUnknown
		}
		if !envelope.Type.ResponseEvent() {
			return TerminalInvalid
		}
		status = envelope.Type.Status()
	}
	switch status {
	case "completed":
		return TerminalCompleted
	case "incomplete":
		var event struct {
			Response struct {
				Incomplete struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if stream && json.Unmarshal(body, &event) == nil && event.Response.Incomplete.Reason == "steered" {
			return TerminalSteered
		}
		return TerminalFailed
	case "failed":
		return TerminalFailed
	case "queued", "in_progress":
		return TerminalPending
	default:
		if stream {
			return TerminalPending
		}
		return TerminalUnknown
	}
}

// MergeTerminal preserves sticky invalid/failure evidence across provider and
// transformed payloads. A later success must not erase an earlier failure.
func MergeTerminal(current, observed TerminalState) TerminalState {
	if current == TerminalInvalid || observed == TerminalInvalid {
		return TerminalInvalid
	}
	if observed == TerminalUnknown {
		return current
	}
	if current == TerminalFailed || observed == TerminalFailed {
		return TerminalFailed
	}
	if current == TerminalSteered || observed == TerminalSteered {
		return TerminalSteered
	}
	if current == TerminalCompleted || observed == TerminalCompleted {
		return TerminalCompleted
	}
	return TerminalPending
}
