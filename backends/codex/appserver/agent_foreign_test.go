package appserver

import (
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

func TestAgentForeignRequestPreservesNativeOriginAndOperation(t *testing.T) {
	text := "Read @recipient.txt"
	request := AgentForeignRequest{
		Source: AgentForeignOrigin{
			RootThreadID: "11111111-1111-4111-8111-111111111111",
			ActorID:      "22222222-2222-4222-8222-222222222222",
			AgentPath:    "/root/worker", TurnID: "turn", CallID: "call",
		},
		TargetThreadID: "33333333-3333-4333-8333-333333333333",
		Operation:     AgentForeignOperation{Type: "message", Message: &text},
	}
	data, err := json.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"operation":{"type":"message","message":"Read @recipient.txt"}`) {
		t.Fatalf("operation shape: %s", data)
	}
	var restored AgentForeignRequest
	if err := json.Unmarshal(data, &restored, json.RejectUnknownMembers(true)); err != nil || !reflect.DeepEqual(restored, request) {
		t.Fatalf("round trip: %#v, %v", restored, err)
	}
}

func TestAgentForeignFailureKeepsKnownRejectionDistinctFromMissingReceipt(t *testing.T) {
	accepted, failure := false, "recipient rejected before admission"
	response := AgentForeignResponse{
		Type: "sideQuestionFailed", TargetThreadID: "33333333-3333-4333-8333-333333333333",
		Accepted: &accepted, Error: &failure,
	}
	data, err := json.Marshal(&response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"accepted":false`) {
		t.Fatalf("known rejection was omitted: %s", data)
	}
	var restored AgentForeignResponse
	if err := json.Unmarshal(data, &restored, json.RejectUnknownMembers(true)); err != nil || !reflect.DeepEqual(restored, response) {
		t.Fatalf("round trip: %#v, %v", restored, err)
	}
	unknown, err := json.Marshal(&AgentForeignResponse{})
	if err != nil || strings.Contains(string(unknown), `"accepted"`) {
		t.Fatalf("unknown receipt invented acceptance: %s, %v", unknown, err)
	}
}
