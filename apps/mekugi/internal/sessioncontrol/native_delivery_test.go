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
