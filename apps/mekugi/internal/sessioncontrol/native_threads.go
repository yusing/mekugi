package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
)

func (r *Requests[T]) Thread(ctx context.Context, thread string) (binding.ThreadInfo, error) {
	raw, err := r.call(ctx, thread, "thread/read", map[string]any{"threadId": thread, "includeTurns": false})
	if err != nil {
		return binding.ThreadInfo{}, err
	}
	info, err := binding.DecodeHistoryThread(raw, thread)
	if err == nil && (info.SessionID == "" || info.Path == "") {
		err = errors.New("host omitted the admitted thread identity or history source")
	}
	return info, err
}

func (r *Requests[T]) Configuration(ctx context.Context, thread, workspace string) (jsontext.Value, error) {
	return r.call(ctx, thread, "config/read", map[string]any{"cwd": workspace, "includeLayers": true})
}

// NativeDelivery is a passive, identity-validated native response item. It has
// no dispatch settings, wait claims or work admission semantics.
type NativeDelivery struct {
	Thread, ID, Sender, Recipient, Kind, Message string
	Outcome                                      string
}

// NativeMessage covers the native agent_message item and tagged contextual
// message form. Provider-authored text without native provenance is not traffic.
type NativeMessage observation.NativeMessage
type nativeMessageText = observation.NativeMessageText

// Delivery consumes only host-recorded plaintext. Native author/recipient fields
// are authoritative; envelope headers cannot impersonate a different actor.
func (item NativeMessage) Delivery() (input NativeDelivery, ok bool) {
	if item.ID == "" || len(item.Content) != 1 || item.Content[0].Type != "input_text" {
		return input, false
	}
	input, ok = nativeAttributedInput(item.Content[0].Text)
	if !ok {
		return input, false
	}
	switch item.Type {
	case "agent_message":
		if item.Author != input.Sender || item.Recipient != input.Recipient {
			return NativeDelivery{}, false
		}
	case "message":
		kind := "multi_agent.inter_agent_message"
		if input.Kind == "FINAL_ANSWER" {
			kind = "multi_agent.inter_agent_completion_message"
		}
		if item.Role != "assistant" || len(item.Metadata.Kinds) != 1 || item.Metadata.Kinds[0] != kind {
			return NativeDelivery{}, false
		}
	default:
		return NativeDelivery{}, false
	}
	input.ID = item.ID
	return input, true
}

func nativeAttributedInput(text string) (input NativeDelivery, ok bool) {
	text, ok = strings.CutPrefix(text, "Message Type: ")
	if !ok {
		return input, false
	}
	input.Kind, text, ok = strings.Cut(text, "\nTask name: ")
	if !ok || input.Kind != "NEW_TASK" && input.Kind != "MESSAGE" && input.Kind != "FINAL_ANSWER" {
		return input, false
	}
	input.Recipient, text, ok = strings.Cut(text, "\nSender: ")
	if !ok || input.Recipient == "" || strings.ContainsAny(input.Recipient, "\r\n") {
		return input, false
	}
	input.Sender, input.Message, ok = strings.Cut(text, "\nPayload:\n")
	if !ok || input.Sender == "" || strings.ContainsAny(input.Sender, "\r\n") {
		return input, false
	}
	return input, true
}
