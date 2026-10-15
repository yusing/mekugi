// Source: internal/responses/items.go:1:34@[48bc03a0] protocol implementation.
package responses

type ItemKind string

const (
	Message        = "message"
	FunctionCall   = "function_call"
	CustomToolCall = "custom_tool_call"
)

// MessageFacts contains protocol identity only. Content eligibility, provenance,
// budgets, and delivery remain consumer policy.
type MessageFacts struct {
	Kind   ItemKind
	Role   string
	Phase  string
	Status string
}

func (m MessageFacts) Assistant() bool { return m.Kind == Message && m.Role == "assistant" }
func (m MessageFacts) FinalAnswerCandidate() bool {
	return m.Assistant() && (m.Phase == "" || m.Phase == "final_answer")
}

// TerminalStatus recognizes body statuses for evidence capture, including
// cancellation. Transport acceptance is deliberately stricter (ObserveTerminal).
func TerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "incomplete", "cancelled":
		return true
	default:
		return false
	}
}
