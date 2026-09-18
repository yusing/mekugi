package router

import (
	"net/http"
	"testing"
)

func TestAuxiliaryAncestryMetadataFailsClosedWithoutRejectingTurn(t *testing.T) {
	for _, raw := range []string{
		`{"request_kind":"turn","agent_name":"/root/a","subagent_kind":"thread_spawn","parent_thread_id":42}`,
		`{"request_kind":"turn","agent_name":"/root/a","subagent_kind":"thread_spawn","thread_id":[]}`,
	} {
		headers := http.Header{codexTurnMetadataHeader: []string{raw}}
		metadata, valid := decodeCodexTurnMetadata(headers)
		if !valid || !metadata.activityIdentityInvalid || metadata.AgentName != "/root/a" {
			t.Fatal(metadata, valid)
		}
	}
	headers := http.Header{codexTurnMetadataHeader: []string{`{"request_kind":"turn","thread_id":"child","parent_thread_id":"parent","agent_name":"/root/a","subagent_kind":"thread_spawn"}`}}
	metadata, valid := decodeCodexTurnMetadata(headers)
	if !valid || metadata.activityIdentityInvalid || metadata.ThreadID != "child" || metadata.ParentThreadID != "parent" {
		t.Fatal(metadata, valid)
	}
}

func TestMalformedAgentAuthorIsAuxiliary(t *testing.T) {
	for _, author := range []string{"42", "[]", "null", "{}"} {
		metadata, valid := decodeCodexTurnMetadata(http.Header{codexTurnMetadataHeader: []string{`{"request_kind":"turn","agent_name":` + author + `,"subagent_kind":"thread_spawn"}`}})
		if !valid || !metadata.activityIdentityInvalid || metadata.AgentName != "" || metadata.commentaryAuthor() != "" {
			t.Fatal(metadata, valid)
		}
		proxy := newManagedMekugiProxy(t)
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}}))
		if err != nil {
			t.Fatal(err)
		}
		transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", metadata, valid)
		if err != nil {
			t.Fatal("auxiliary author rejected request", err)
		}
		transform.Close()
	}
}
