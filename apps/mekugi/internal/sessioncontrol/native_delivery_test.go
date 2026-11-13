package sessioncontrol

import "testing"

func nativeMessageFixture(kind, body string) NativeMessage {
	return NativeMessage{Type: "agent_message", ID: "amsg_child", Author: "/root/child", Recipient: "/root", Content: []nativeMessageText{{Type: "input_text", Text: "Message Type: " + kind + "\nTask name: /root\nSender: /root/child\nPayload:\n" + body}}}
}

func TestNativeMessageDeliveryRequiresOwnedEnvelope(t *testing.T) {
	for name, mutate := range map[string]func(*NativeMessage){
		"foreign author":     func(item *NativeMessage) { item.Author = "/root/other" },
		"foreign recipient":  func(item *NativeMessage) { item.Recipient = "/root/other" },
		"missing identity":   func(item *NativeMessage) { item.ID = "" },
		"ordinary assistant": func(item *NativeMessage) { item.Type = "message"; item.Role = "assistant" },
		"output text":        func(item *NativeMessage) { item.Content[0].Type = "output_text" },
		"extra content":      func(item *NativeMessage) { item.Content = append(item.Content, item.Content[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			item := nativeMessageFixture("MESSAGE", "body")
			mutate(&item)
			if input, ok := item.Delivery(); ok {
				t.Fatalf("unowned envelope became traffic: %+v", input)
			}
		})
	}
}

func TestNativeMessageSupportedCarriersPreservePayload(t *testing.T) {
	for _, kind := range []string{"NEW_TASK", "MESSAGE", "FINAL_ANSWER"} {
		for _, shape := range []string{"agent_message", "message"} {
			t.Run(kind+"/"+shape, func(t *testing.T) {
				body := "Status: failed\nStatus: interrupted\nPayload:\nverbatim final\n"
				item := nativeMessageFixture(kind, body)
				if shape == "message" {
					item.Type, item.Role = "message", "assistant"
					provenance := "multi_agent.inter_agent_message"
					if kind == "FINAL_ANSWER" {
						provenance = "multi_agent.inter_agent_completion_message"
					}
					item.Metadata.Kinds = []string{provenance}
				}
				input, ok := item.Delivery()
				if !ok || input.Message != body || input.Outcome != "" || input.ID != item.ID || input.Sender != item.Author {
					t.Fatalf("native payload/identity changed or inferred outcome: %+v %t", input, ok)
				}
				if shape == "message" {
					item.Metadata.Kinds = []string{"other"}
					if _, ok := item.Delivery(); ok {
						t.Fatal("wrong contextual provenance accepted")
					}
					item.Metadata.Kinds = []string{"multi_agent.inter_agent_message"}
					item.Role = "user"
					if _, ok := item.Delivery(); ok {
						t.Fatal("human role accepted")
					}
				}
			})
		}
	}
}
