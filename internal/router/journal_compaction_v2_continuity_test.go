package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func deliverV2ContinuityReset(t *testing.T, proxy *mekugiProxy, workspace, thread string) (map[string]jsonv1.RawMessage, journalCompactionRecovery) {
	t.Helper()
	request, headers := journalCompactionV2Request(t, workspace, thread)
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "continuity-reset", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("local reset forwarded to provider")
	}
	for line := range strings.SplitSeq(output.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID     string                         `json:"id"`
				Output []map[string]jsonv1.RawMessage `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &event) != nil || event.Type != "response.completed" {
			continue
		}
		if len(event.Response.Output) != 1 || jsonString(event.Response.Output[0], "type") != "compaction" {
			t.Fatal("reset did not deliver exactly one compaction item")
		}
		retained, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalCompactionRecoveryName(workspace, thread, event.Response.ID)))
		if err != nil {
			t.Fatal(err)
		}
		var recovery journalCompactionRecovery
		if err := json.Unmarshal(retained, &recovery); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(recovery.Reference, journalCompactionReferencePrefix) || recovery.Reference != jsonString(event.Response.Output[0], "encrypted_content") || recovery.Text == "" {
			t.Fatal("delivered reference lacks exact retained recovery")
		}
		return event.Response.Output[0], recovery
	}
	t.Fatal("missing completed reset response")
	return nil, journalCompactionRecovery{}
}

func reopenV2ContinuityProxy(t *testing.T, proxy *mekugiProxy) *mekugiProxy {
	t.Helper()
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.snapshots.close)
	fresh := newManagedMekugiProxy(t)
	fresh.replayStore, fresh.journalCompaction = reopened, "auto"
	return fresh
}

func v2ContinuityRequest(t *testing.T, workspace, thread, parent, fork string, item map[string]jsonv1.RawMessage) (parsedResponsesRequest, http.Header) {
	t.Helper()
	request := serverRequest(t, func(fields map[string]any) {
		fields["input"] = []any{item, map[string]any{"type": "compaction", "encrypted_content": "provider-ciphertext"}, map[string]any{"role": "user", "content": "Continue after reset."}, testFlatCodeModeAdditionalTools(testCodeModeDescription)}
	})
	headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.ThreadID, metadata.ParentThreadID, metadata.ForkedFromThreadID = thread, parent, fork
	if parent != "" {
		metadata.SubagentKind = "thread_spawn"
	}
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	headers.Set(threadIDHeader, thread)
	return request, headers
}

func requireV2ContinuityForward(t *testing.T, proxy *mekugiProxy, workspace, thread, parent, fork string, item map[string]jsonv1.RawMessage, want string) {
	t.Helper()
	request, headers := v2ContinuityRequest(t, workspace, thread, parent, fork, item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "continuity-forward", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	requireV2SummaryForward(t, provider.forwarded, want)
}

func requireV2SummaryForward(t *testing.T, requests [][]byte, want string) {
	t.Helper()
	if len(requests) != 1 || bytes.Contains(requests[0], []byte(journalCompactionReferencePrefix)) {
		t.Fatal("recovery was not expanded before forwarding")
	}
	var forwarded struct {
		Input []map[string]jsonv1.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(requests[0], &forwarded); err != nil {
		t.Fatal(err)
	}
	var content []struct {
		Text string `json:"text"`
	}
	if len(forwarded.Input) < 2 {
		t.Fatal("forwarded recovery missing")
	}
	if err := json.Unmarshal(forwarded.Input[0]["content"], &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0].Text != want || jsonString(forwarded.Input[0], "role") != "assistant" {
		t.Fatalf("retained recovery changed: %v", content)
	}
	if jsonString(forwarded.Input[1], "encrypted_content") != "provider-ciphertext" {
		t.Fatal("provider ciphertext changed")
	}
}

func TestJournalCompactionV2ContinuityDurableHistories(t *testing.T) {
	t.Parallel()
	for _, history := range []string{"resume", "fork", "native-child"} {
		t.Run(history, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			source := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, source, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Original exact recovery")}}); err != nil {
				t.Fatal(err)
			}
			item, recovery := deliverV2ContinuityReset(t, proxy, workspace, source)
			transform.Close()
			proxy = reopenV2ContinuityProxy(t, proxy)
			thread, parent, fork := source, "", ""
			if history == "fork" {
				thread, fork = "fork-thread", source
			}
			if history == "native-child" {
				thread, parent = "native-child-thread", source
			}
			requireV2ContinuityForward(t, proxy, workspace, thread, parent, fork, item, recovery.Text)
			if history != "resume" {
				// Resume inherited history with only the requesting thread.
				proxy = reopenV2ContinuityProxy(t, proxy)
				requireV2ContinuityForward(t, proxy, workspace, thread, "", "", item, recovery.Text)
			}
			if history == "fork" {
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, source, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Parent allocation after fork")}}); err != nil {
					t.Fatal(err)
				}
				later, _ := deliverV2ContinuityReset(t, proxy, workspace, source)
				request, headers := v2ContinuityRequest(t, workspace, thread, "", "", later)
				provider := &serverFakeProvider{}
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "late-parent", provider, &output, nil, proxy); err == nil || len(provider.forwarded) != 0 {
					t.Fatal("frozen fork accepted a later parent allocation")
				}
			}
		})
	}
}

func TestJournalCompactionV2ContinuityUnavailableRecoveryFailsBeforeProvider(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"unrelated-thread", "unrelated-workspace", "missing-record", "corrupt-identity"} {
		t.Run(boundary, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Live journal cannot replace retained reset")}}); err != nil {
				t.Fatal(err)
			}
			item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread)
			path := filepath.Join(proxy.replayStore.directory, journalCompactionRecoveryName(workspace, thread, recovery.ResponseID))
			switch boundary {
			case "unrelated-thread":
				thread = "unrelated-thread"
			case "unrelated-workspace":
				workspace = t.TempDir()
			case "missing-record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt-identity":
				recovery.Namespace = "unrelated-namespace"
				if err := os.WriteFile(path, mustTestJSON(t, recovery), 0600); err != nil {
					t.Fatal(err)
				}
			}
			transform.Close()
			proxy = reopenV2ContinuityProxy(t, proxy)
			request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
			provider := &serverFakeProvider{}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "unavailable-recovery", provider, &output, nil, proxy); err == nil {
				t.Fatal("unavailable recovery was accepted")
			}
			if len(provider.forwarded) != 0 {
				t.Fatal("unavailable recovery reached provider")
			}
		})
	}
}

func TestJournalCompactionV2ContinuityFailedDeliveryPreservesHookRecovery(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Hook recovery remains available")}}); err != nil {
		t.Fatal(err)
	}
	before, err := proxy.replayStore.postCompactContext(t.Context(), workspace, thread)
	if err != nil || before == "" {
		t.Fatalf("hook recovery: %q %v", before, err)
	}
	request, headers := journalCompactionV2Request(t, workspace, thread)
	provider := &serverFakeProvider{}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "failed-v2-delivery", provider, serverErrorWriter{err: io.ErrClosedPipe}, nil, proxy); err == nil {
		t.Fatal("downstream failure was swallowed")
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("failed local delivery triggered provider")
	}
	proxy = reopenV2ContinuityProxy(t, proxy)
	after, err := proxy.replayStore.postCompactContext(t.Context(), workspace, thread)
	if err != nil || after != before {
		t.Fatalf("failed delivery changed hook recovery: %q %v", after, err)
	}
	if proxy.replayStore.answeredCompaction(t.Context(), workspace, thread, "unrelated-provider-response") {
		t.Fatal("failed reset suppressed unrelated hook recovery")
	}
}

func TestJournalCompactionV2ContinuityWebSocketPrewarm(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread)
	transform.Close()
	proxy = reopenV2ContinuityProxy(t, proxy)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "v2-prewarm")
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		for i, id := range []string{"warm", "continued"} {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if bytes.Contains(request["input"], []byte(journalCompactionReferencePrefix)) {
				t.Error("WebSocket history exposed a local recovery reference")
			}
			if i == 0 {
				var input []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(request["input"], &input); err != nil || len(input) != 1 || len(input[0].Content) != 1 || input[0].Content[0].Text != recovery.Text || string(request["generate"]) != "false" {
					t.Errorf("prewarm lost exact reset context: %s", request["input"])
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	metadata := codexTurnMetadata{RequestKind: "prewarm", ThreadID: thread, Directories: map[string]jsonv1.RawMessage{workspace: nil}}
	clientMetadata := map[string]string{codexTurnMetadataHeader: string(mustTestJSON(t, metadata)), threadIDHeader: thread}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "generate": false, "input": []any{item}, "client_metadata": clientMetadata})
	if result := socketRead(t, ctx, conn); jsonString(result, "type") != "response.completed" {
		t.Fatalf("prewarm failed: %s", mustTestJSON(t, result))
	}
	metadata.RequestKind = "turn"
	clientMetadata[codexTurnMetadataHeader] = string(mustTestJSON(t, metadata))
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "previous_response_id": "warm", "input": []any{map[string]any{"role": "user", "content": "Continue."}}, "tools": testExecResponsesTools(), "client_metadata": clientMetadata})
	if result := socketRead(t, ctx, conn); jsonString(result, "type") != "response.completed" {
		t.Fatalf("incremental continuation failed: %s", mustTestJSON(t, result))
	}
}
