package router

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func activityAdmissionRequest(t *testing.T, input []any) codexresponses.Request {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestUnnamedRecipientPreservesUnretainedPrefixMessage(t *testing.T) {
	generated := assistantCommentaryMessage(binding.SubagentMessageID("old"), "Old notice.")
	envelope := map[string]any{"type": "agent_message", "author": "/root/sender", "content": []any{map[string]any{"type": "encrypted_content"}}}
	fields := map[string]json.RawMessage{"input": mustTestJSON(t, []any{generated, envelope})}
	if got := prepareSubagentInputEnvelopes(fields, "").replies; len(got) != 0 {
		t.Fatal("absent recipient matched absent identity", got)
	}
	if !bytes.Equal(fields["input"], mustTestJSON(t, []any{generated, envelope})) {
		t.Fatal("unretained prefix-matching message or original envelope was removed")
	}
}
