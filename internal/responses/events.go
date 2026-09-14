// Package responses owns wire-level Responses event facts, not feature policy,
// transport lifetimes, or execution. Unknown wire values remain representable.
package responses

import "strings"

type Kind string

const (
	Created                = "response.created"
	Completed              = "response.completed"
	Failed                 = "response.failed"
	Incomplete             = "response.incomplete"
	InProgress             = "response.in_progress"
	OutputItemAdded        = "response.output_item.added"
	OutputItemDone         = "response.output_item.done"
	FunctionArgumentsDelta = "response.function_call_arguments.delta"
	FunctionArgumentsDone  = "response.function_call_arguments.done"
	CustomInputDelta       = "response.custom_tool_call_input.delta"
	CustomInputDone        = "response.custom_tool_call_input.done"
	OutputTextDelta        = "response.output_text.delta"
	OutputTextDone         = "response.output_text.done"
	ContentPartAdded       = "response.content_part.added"
	ContentPartDone        = "response.content_part.done"
	Error                  = "error"
	Metadata               = "codex.response.metadata"
	RateLimits             = "codex.rate_limits"
	WebSocketTiming        = "responsesapi.websocket_timing"
	Create                 = "response.create"
	Steer                  = "response.steer"
	SteerAccepted          = "response.steer.accepted"
	SteerFailed            = "response.steer.failed"
)

// Terminal reports a response terminal, not successful request or delivery.
func (k Kind) Terminal() bool {
	return k == Completed || k == Failed || k == Incomplete
}

func (k Kind) EndsExchange() bool { return k.Terminal() || k == Error }
func (k Kind) ItemEvent() bool    { return k == OutputItemAdded || k == OutputItemDone }

// Source: codex-rs/codex-api/src/endpoint/responses_websocket.rs:749:777 and sse/responses.rs:524:535.
func (k Kind) Ancillary() bool { return k == Metadata || k == RateLimits || k == WebSocketTiming }

// ResponseFamily is the permissive correlation prefix, including a bare
// response. prefix. It does not establish a valid response event or terminal.
func (k Kind) ResponseFamily() bool { return strings.HasPrefix(string(k), "response.") }
func (k Kind) ResponseEvent() bool {
	return k.ResponseFamily() && k != "response."
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

// FunctionArguments identifies protocol argument fragments. Recognition does not
// authorize evaluation; consumers still require complete, validated calls.
func (k Kind) FunctionArguments() bool {
	return k == FunctionArgumentsDelta || k == FunctionArgumentsDone
}

func (k Kind) ContentPart() bool { return k == ContentPartAdded || k == ContentPartDone }
