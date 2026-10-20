package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func activityAdmissionRequest(t *testing.T, input []any) parsedResponsesRequest {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestUnnamedRecipientPreservesUnretainedPrefixMessage(t *testing.T) {
	generated := assistantCommentaryMessage(subagentCommentaryMessageID("old"), "Old notice.")
	envelope := map[string]any{"type": "agent_message", "author": "/root/sender", "content": []any{map[string]any{"type": "encrypted_content"}}}
	fields := map[string]json.RawMessage{"input": mustTestJSON(t, []any{generated, envelope})}
	if got := prepareSubagentInputEnvelopes(fields, "").replies; len(got) != 0 {
		t.Fatal("absent recipient matched absent identity", got)
	}
	if !bytes.Equal(fields["input"], mustTestJSON(t, []any{generated, envelope})) {
		t.Fatal("unretained prefix-matching message or original envelope was removed")
	}
}
