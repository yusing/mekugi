package responses

type ItemKind string

const (
	Message        = "message"
	FunctionCall   = "function_call"
	CustomToolCall = "custom_tool_call"
)

// ToolCall identifies the two callable item encodings, not whether their
// arguments are complete, validated, available, or authorized for execution.
func (k ItemKind) ToolCall() bool { return k == FunctionCall || k == CustomToolCall }

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
func (m MessageFacts) CompletedCommentary() bool {
	return m.Assistant() && m.Phase == "commentary" && m.Status == "completed"
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
