package router

import (
	"bytes"
	jsonv1 "encoding/json"
	"net/http"
	"testing"
)

func journalCompactionV2Request(t *testing.T, workspace, thread string) (parsedResponsesRequest, http.Header) {
	t.Helper()
	request, headers := journalCompactionRequest(t, workspace, thread)
	request.fields["input"] = mustTestJSON(t, []any{map[string]any{"role": "user", "content": "Summarize."}, map[string]any{"type": "compaction_trigger"}})
	request.fields["tools"] = mustTestJSON(t, []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}})
	request.fields["parallel_tool_calls"] = mustTestJSON(t, true)
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.Compaction = mustTestJSON(t, map[string]any{"trigger": "auto", "reason": "context_limit", "implementation": "responses_compaction_v2", "phase": "mid_turn", "strategy": "memento"})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	return request, headers
}

func TestJournalCompactionV2PreservesProviderPolicy(t *testing.T) {
	for _, mode := range []string{"off", "slice"} {
		t.Run(mode, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			proxy.journalCompaction = mode
			request, headers := journalCompactionV2Request(t, workspace, transform.shellThreadID)
			original, err := request.wireBody(request.fields)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := journalCompactionItemSSE("native-provider", "gpt-test", map[string]any{"type": "compaction", "encrypted_content": "provider-owned-ciphertext"})
			if err != nil {
				t.Fatal(err)
			}
			response := serverHTTPResponse(string(wire))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "v2-provider-policy", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) || !bytes.Equal(output.Bytes(), wire) {
				t.Fatal("native provider compaction request or response changed")
			}
		})
	}
}

func TestJournalCompactionV2RestoresExactSummaryBeforeProvider(t *testing.T) {
	for _, path := range []string{"ordinary", "execution-free", "provider-compaction"} {
		t.Run(path, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Keep the exact reset fact")}}); err != nil {
				t.Fatal(err)
			}
			item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread)
			provider := &serverFakeProvider{}
			var output bytes.Buffer
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Later fact must not replace the reset")}}); err != nil {
				t.Fatal(err)
			}
			cipher := map[string]any{"type": "compaction", "encrypted_content": "provider-ciphertext"}
			input := []any{item, cipher, map[string]any{"role": "user", "content": "Continue."}}
			request := serverRequest(t, func(fields map[string]any) {
				if path == "ordinary" {
					input = append(input, testFlatCodeModeAdditionalTools(testCodeModeDescription))
				}
				fields["input"] = input
				if path == "execution-free" {
					delete(fields, "tools")
				}
			})
			headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
			provider.results = []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}
			if path == "provider-compaction" {
				request, headers = journalCompactionV2Request(t, workspace, thread)
				request.fields["input"] = mustTestJSON(t, append(input, map[string]any{"type": "compaction_trigger"}))
				metadata, _ := decodeCodexTurnMetadata(headers)
				metadata.Directories = nil // Native V2 compaction omits workspace.
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
				wire, err := journalCompactionItemSSE("provider-response", "gpt-test", cipher)
				if err != nil {
					t.Fatal(err)
				}
				response := serverHTTPResponse(string(wire))
				response.Header.Set("Content-Type", "text/event-stream")
				provider.results = []serverForwardResult{{response: response}}
			}
			proxy.journalCompaction = "off"
			if err := executeRequest(t.Context(), t.Context(), request, headers, "v2-continue", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			requireV2SummaryForward(t, provider.forwarded, recovery.Text)
		})
	}
}
