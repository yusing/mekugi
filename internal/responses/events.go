// Package responses owns wire-level Responses event facts, not feature policy,
// transport lifetimes, or execution. Unknown wire values remain representable.
package responses

import "strings"

type Kind string

const (
	Created                Kind = "response.created"
	Completed              Kind = "response.completed"
	Failed                 Kind = "response.failed"
	Incomplete             Kind = "response.incomplete"
	InProgress             Kind = "response.in_progress"
	OutputItemAdded        Kind = "response.output_item.added"
	OutputItemDone         Kind = "response.output_item.done"
	FunctionArgumentsDelta Kind = "response.function_call_arguments.delta"
	FunctionArgumentsDone  Kind = "response.function_call_arguments.done"
	CustomInputDelta       Kind = "response.custom_tool_call_input.delta"
	CustomInputDone        Kind = "response.custom_tool_call_input.done"
	OutputTextDelta        Kind = "response.output_text.delta"
	OutputTextDone         Kind = "response.output_text.done"
	ContentPartAdded       Kind = "response.content_part.added"
	ContentPartDone        Kind = "response.content_part.done"
	Error                  Kind = "error"
	Metadata               Kind = "codex.response.metadata"
	RateLimits             Kind = "codex.rate_limits"
	WebSocketTiming        Kind = "responsesapi.websocket_timing"
	Create                 Kind = "response.create"
	Steer                  Kind = "response.steer"
	SteerAccepted          Kind = "response.steer.accepted"
	SteerFailed            Kind = "response.steer.failed"
)

// Terminal reports a response terminal, not successful request or delivery.
func (k Kind) Terminal() bool {
	return k == Completed || k == Failed || k == Incomplete
}

func (k Kind) EndsExchange() bool { return k.Terminal() || k == Error }
func (k Kind) ItemEvent() bool    { return k == OutputItemAdded || k == OutputItemDone }
func (k Kind) Ancillary() bool    { return k == Metadata || k == RateLimits || k == WebSocketTiming }
func (k Kind) ResponseEvent() bool {
	return strings.HasPrefix(string(k), "response.") && k != "response."
}
func (k Kind) Steering() bool { return k == Steer || strings.HasPrefix(string(k), string(Steer)+".") }

type RequestKind string

const (
	Turn       RequestKind = "turn"
	Prewarm    RequestKind = "prewarm"
	Compaction RequestKind = "compaction"
)

func (k RequestKind) Known() bool     { return k == Turn || k == Prewarm || k == Compaction }
func (k RequestKind) Scheduled() bool { return k == Turn || k == Compaction }
