// Package chat classifies Chat Completions wire facts. It does not accumulate
// chunks, validate executable calls, synthesize Responses events, or own capture.
package chat

type FinishReason string

const (
	Stop          FinishReason = "stop"
	ToolCalls     FinishReason = "tool_calls"
	Length        FinishReason = "length"
	ContentFilter FinishReason = "content_filter"
)

// ResponseStatus reports recognized completion evidence, not usable output.
// Unknown and absent reasons must not be treated as completion.
func (r FinishReason) ResponseStatus() string {
	switch r {
	case Stop, ToolCalls:
		return "completed"
	case Length, ContentFilter:
		return "incomplete"
	default:
		return ""
	}
}

func (r FinishReason) IncompleteReason() string {
	switch r {
	case Length:
		return "max_output_tokens"
	case ContentFilter:
		return "content_filter"
	default:
		return ""
	}
}
