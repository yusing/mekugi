package sessioncontrol

import (
	"testing"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeMessageDeliveryRequiresOwnedEnvelope(t *testing.T) {
	for name, mutate := range map[string]func(*NativeMessage){
		"missing metadata": func(item *NativeMessage) { item.Metadata.Kinds = nil },
		"wrong provenance": func(item *NativeMessage) { item.Metadata.Kinds[0] = "multi_agent.inter_agent_completion_message" },
		"foreign identity": func(item *NativeMessage) { item.ID = "ordinary-item" },
		"human role":       func(item *NativeMessage) { item.Role = "user" },
		"assignment": func(item *NativeMessage) {
			*item, _, _ = nativeContent(control.NativeInput{ID: "assignment", Kind: "NEW_TASK", Sender: "/root", Recipient: "/root/child"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			item, _, _ := nativeContent(control.NativeInput{ID: "message", Kind: "MESSAGE", Sender: "/root/child", Recipient: "/root", Message: "body"})
			mutate(&item)
			if input, ok := item.Delivery(); ok {
				t.Fatalf("unowned envelope became delivered traffic: %+v", input)
			}
		})
	}
}

func TestNativeCompletionDeliveryRetainsOutcome(t *testing.T) {
	for _, status := range []control.Status{control.Completed, control.Interrupted, control.Failed} {
		body := "Status: failed\nStatus: interrupted\nfinal body"
		item, _, _ := nativeContent(control.NativeInput{ID: string(status), Kind: "FINAL_ANSWER", Sender: "/root/child", Recipient: "/root", Message: body, Outcome: status, SourceGeneration: 2, ParentTurn: "child-turn"})
		input, ok := item.Delivery()
		if !ok || input.Outcome != status || input.Message != body || input.SourceGeneration != 2 || input.ParentTurn != "child-turn" {
			t.Fatalf("retained completion outcome = %+v, valid=%t", input, ok)
		}
	}
}

func TestNativeLegacyCompletionDoesNotInferOutcome(t *testing.T) {
	item, _, _ := nativeContent(control.NativeInput{ID: "old", Kind: "FINAL_ANSWER", Sender: "/root/child", Recipient: "/root", Message: "Status: interrupted\nExample"})
	input, ok := item.Delivery()
	if !ok || input.Outcome != "" || input.SourceGeneration != 0 || input.Message != "Status: interrupted\nExample" {
		t.Fatalf("legacy payload acquired unsupported outcome: %+v", input)
	}
}
