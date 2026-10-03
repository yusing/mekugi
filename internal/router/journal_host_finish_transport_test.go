package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJournalHelperFinishBatchPersistsOnlyAcceptedReceipt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		mutations []journalMutation
		want      bool
	}{
		{"finish only", []journalMutation{{Op: "finish"}}, true},
		{"final report", []journalMutation{{Op: "add", Title: new("Checks passed")}, {Op: "finish"}}, true},
		{"finish is not last", []journalMutation{{Op: "finish"}, {Op: "add", Title: new("must roll back")}}, false},
		{"unsupported finish operand", []journalMutation{{Op: "add", Title: new("must roll back")}, {Op: "finish", Title: new("ignored")}}, false},
		{"rejected mutation", []journalMutation{{Op: "set", P: "/missing", State: new("done")}, {Op: "finish"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			transform.shellTurnID = "final-turn"
			token := testRuntimeCommentaryCall(t, transform, "final-work")
			body, err := json.Marshal(map[string]any{"journal": test.mutations, "id": "publication"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			proxy.commentary.serveHTTP(response, request)
			var outcome struct {
				OK    bool     `json:"ok"`
				Items []string `json:"items"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &outcome) != nil || outcome.OK != test.want {
				t.Fatalf("publication = %d %s", response.Code, response.Body)
			}
			if test.want && outcome.Items == nil {
				t.Fatal("finish must return an empty path array, not null")
			}
			found := false
			if err := proxy.journals.transaction(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, func(j *threadJournal, exists bool) error {
				_, found = j.Receipts["runtime:"+journalHostFinishReceipt(transform.shellTurnID, "final-work")]
				if !test.want && len(j.Items) != 0 {
					t.Fatalf("rejected completion leaked journal mutations: %+v", j.Items)
				}
				return errJournalUnchanged
			}); err != nil {
				t.Fatal(err)
			}
			if found != test.want {
				t.Fatalf("finish receipt = %v, want %v", found, test.want)
			}
		})
	}
}

func TestJournalNativeHostFinishSkipsProviderOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		for _, test := range []struct {
			name, result string
			local        bool
		}{
			{"successful", "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\nverified", true},
			{"nonzero", "Wall time: 0.1 seconds\nProcess exited with code 7\nOutput:\nfailed", false},
			{"yielded", "Wall time: 0.1 seconds\nProcess running with session ID 7\nOutput:\n", false},
			{"cancelled", "User cancelled tool", false},
		} {
			t.Run(test.name+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				attachTestReplayStore(t, proxy)
				workspace := t.TempDir()
				nativeTools := testNativeResponsesTools()
				nativeTools[0].(map[string]any)["parameters"] = map[string]any{
					"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}},
				}
				headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
				metadata, _ := decodeCodexTurnMetadata(headers)
				metadata.TurnID = "final-turn"
				headers.Set(codexTurnMetadataHeader, string(mustMarshalJSON(metadata)))
				call := map[string]any{"type": "function_call", "id": "final-work-item", "call_id": "final-work", "name": "exec_command", "status": "completed", "arguments": `{"cmd":"printf verified","journal":[{"op":"log","text":"Final work requested"},{"op":"finish"}]}`}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, stream, "completed", "full", call)}}}
				request := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = stream
					fields["tools"] = nativeTools
					fields["input"] = []any{map[string]any{"role": "user", "content": "Complete the final check"}}
				})
				var first bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &first, NewCriticalErrors(), proxy); err != nil {
					t.Fatal(err)
				}
				var host map[string]jsonv1.RawMessage
				for _, item := range journalFinishClientOutput(t, stream, first.Bytes()) {
					if jsonString(item, "call_id") == "final-work" {
						host = item
					}
				}
				if host == nil || strings.Contains(jsonString(host, "arguments"), "journal") {
					t.Fatalf("host arguments did not preserve stock execution: %s", first.Bytes())
				}
				before := proxy.usage.roundtrips("thread-1")
				continuation := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = stream
					fields["tools"] = nativeTools
					fields["input"] = []any{host, map[string]any{"type": "function_call_output", "call_id": "final-work", "output": test.result}}
				})
				fallback := map[string]any{"type": "message", "id": "interpreted", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Provider interpretation is required."}}}
				nextProvider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, stream, "completed", "full", fallback)}}}
				var next bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), continuation, headers, "session", nextProvider, &next, NewCriticalErrors(), proxy); err != nil {
					t.Fatal(err)
				}
				want := 1
				if test.local {
					want = 0
				}
				if len(nextProvider.forwarded) != want {
					t.Fatalf("continuation provider requests=%d, want %d: %s", len(nextProvider.forwarded), want, next.Bytes())
				}
				if test.local && (!strings.Contains(next.String(), "Final work requested") || strings.Contains(next.String(), "Done.")) {
					t.Fatalf("local completion lost its report or added an acknowledgment: %s", next.Bytes())
				}
				if got := proxy.usage.roundtrips("thread-1"); got != before+uint64(want) {
					t.Fatalf("local response counted as provider consumption: before=%d after=%d requests=%d", before, got, want)
				}
			})
		}
	}
}
