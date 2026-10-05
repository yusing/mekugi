package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func journalCompactionRequest(t *testing.T, workspace, thread string) (parsedResponsesRequest, http.Header) {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
		"access_programs": map[string]string{"cyber": "standard"},
		"input":           []any{map[string]any{"role": "user", "content": "Summarize."}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	headers := serverCompactionMetadataHeaders(t)
	metadata, ok := decodeCodexTurnMetadata(headers)
	if !ok {
		t.Fatal("invalid fixture metadata")
	}
	metadata.ThreadID = thread
	metadata.Directories = map[string]jsonv1.RawMessage{workspace: nil}
	headers = make(http.Header)
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	headers.Set(threadIDHeader, thread)
	return request, headers
}

func TestJournalCompactionDeliversDurableSummaryWithoutProvider(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Continue parser"), State: new("working")},
	}); err != nil {
		t.Fatal(err)
	}
	request, headers := journalCompactionRequest(t, workspace, thread)
	// Real Codex local compaction omits workspaces. Recovery must not depend
	// on this router process retaining the preceding ordinary request.
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.Directories = nil
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = reopened
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 0 || !strings.Contains(output.String(), "Continue parser") || !strings.Contains(output.String(), "response.completed") {
		t.Fatalf("synthesis not delivered: requests=%d output=%s", len(provider.forwarded), &output)
	}
	data, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalCompactionName(workspace, thread)))
	if err != nil {
		t.Fatal(err)
	}
	var record journalCompactionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	restarted, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), record.ResponseID) || !restarted.answeredCompaction(t.Context(), workspace, thread, record.ResponseID) {
		t.Fatal("delivered response has no durable provenance after restart")
	}
	for _, key := range [][3]string{{workspace, thread, "different"}, {workspace, "other", record.ResponseID}, {t.TempDir(), thread, record.ResponseID}} {
		if restarted.answeredCompaction(t.Context(), key[0], key[1], key[2]) {
			t.Fatal("synthesis provenance crossed response/thread/workspace identity")
		}
	}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact-failed", provider, serverErrorWriter{err: io.ErrClosedPipe}, nil, proxy); err == nil {
		t.Fatal("downstream failure was swallowed")
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("failed local delivery replayed compaction upstream")
	}
}

func TestJournalCompactionFallbackPreservesProviderRequest(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"off", "slice", "missing-thread", "conflicted-header", "conflicted-headers", "ambiguous-workspace", "ambiguous-history", "missing-evidence", "conflicted-journal", "oversize-summary", "record-failure"} {
		t.Run(scenario, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			request, headers := journalCompactionRequest(t, workspace, thread)
			metadata, _ := decodeCodexTurnMetadata(headers)
			switch scenario {
			case "off", "slice":
				proxy.journalCompaction = scenario
			case "missing-thread":
				headers.Del(threadIDHeader)
			case "conflicted-header":
				metadata.ThreadID = "foreign"
			case "conflicted-headers":
				headers.Add(threadIDHeader, "foreign")
			case "ambiguous-workspace":
				metadata.Directories[t.TempDir()] = nil
			case "ambiguous-history":
				metadata.Directories = nil
				if err := proxy.journals.initialize(transform.ctx, proxy.replayStore, t.TempDir(), thread, "/root", ""); err != nil {
					t.Fatal(err)
				}
			case "missing-evidence":
				metadata.ThreadID = "unknown"
				headers.Set(threadIDHeader, "unknown")
			case "conflicted-journal":
				if err := proxy.journals.transaction(transform.ctx, proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error { j.IdentityConflicted = true; return nil }); err != nil {
					t.Fatal(err)
				}
			case "oversize-summary":
				for range 4 {
					if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new(strings.Repeat("Constraint ", 1200))}}); err != nil {
						t.Fatal(err)
					}
				}
			case "record-failure":
				if err := os.Mkdir(filepath.Join(proxy.replayStore.directory, journalCompactionName(workspace, thread)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
			original, err := request.wireBody(request.fields)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := journalCompactionSSE("provider-compact", "gpt-test", "Provider recovery")
			if err != nil {
				t.Fatal(err)
			}
			response := serverHTTPResponse(string(wire))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) || !bytes.Equal(output.Bytes(), wire) {
				t.Fatalf("fallback altered native request/response: forwarded=%q output=%s", provider.forwarded, &output)
			}
		})
	}
}

func TestJournalCompactionUsesChangeOnlyEvidence(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.journalCompaction = "auto"
	workspace, thread := t.TempDir(), "change-only"
	ctx, release, err := proxy.replayStore.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	store := proxy.replayStore.scoped(ctx)
	id, err := store.reserveChange(ctx, workspace, thread, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"edit": {
		ChangeID: id, CorrelationID: "edit", ExecutingThread: thread,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("change.txt", "change.txt", "before\n", "after\n")},
	}}); err != nil {
		t.Fatal(err)
	}
	request, headers := journalCompactionRequest(t, workspace, thread)
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 0 || !strings.Contains(output.String(), id) || !strings.Contains(output.String(), "change.txt") {
		t.Fatalf("change-only recovery lost evidence: %s", &output)
	}
}

func TestCompactedResponseIDUsesLatestCompleteRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	for _, test := range []struct{ content, want string }{
		{`{"type":"compacted","payload":{"compaction_response_id":"one"}}`, "one"},
		{"{\"type\":\"compacted\",\"payload\":{\"compaction_response_id\":\"one\"}}\n{\"type\":\"compacted\",\"payload\":{\"compaction_response_id\":\"two\"}}\n", "two"},
		{"{\"type\":\"compacted\",\"payload\":{\"compaction_response_id\":\"old\"}}\n{\"type\":\"compacted\",\"payload\":{}}\n", ""},
		{"{\"type\":\"compacted\",\"payload\":{\"compaction_response_id\":\"old\"}}\n{", ""},
	} {
		if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := compactedResponseID(path); got != test.want {
			t.Fatalf("got response %q, want %q", got, test.want)
		}
	}
	if compactedResponseID("relative") != "" || compactedResponseID(t.TempDir()) != "" {
		t.Fatal("accepted a relative path or non-file")
	}
}

func TestJournalCompactionFlagGate(t *testing.T) {
	t.Parallel()
	flags := newRouterFlags(io.Discard)
	if *flags.journalCompaction != "off" {
		t.Fatal("auto was enabled without paid evaluation")
	}
	for _, value := range []string{"auto", "slice", "off"} {
		if _, _, err := SplitCommand([]string{"--journal-compaction=" + value, "codex", "exec"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"--journal-compaction=invalid"}, {"--mode=passthrough", "--journal-compaction=auto"}} {
		if err := RunSession(t.Context(), args, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "--journal-compaction") {
			t.Fatalf("flag validation: %v", err)
		}
	}
	if _, err := journalCompactionSSE("id", "model", "  "); err == nil {
		t.Fatal("empty summary was accepted")
	}
}

func TestJournalCompactionPreservesRoutingPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "auto"} {
		for _, failed := range []bool{false, true} {
			name := mode + "/completed"
			if failed {
				name = mode + "/delivery-failed"
			}
			t.Run(name, func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				thread := transform.shellThreadID
				proxy.journalCompaction = mode
				request, headers := journalCompactionRequest(t, workspace, thread)
				request.fields["model"] = mustTestJSON(t, "gpt-5.6-terra")
				wire, err := journalCompactionSSE("upstream", "gpt-6-sol", "Provider summary")
				if err != nil {
					t.Fatal(err)
				}
				response := serverHTTPResponse(string(wire))
				response.Header.Set("Content-Type", "text/event-stream")
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				var output io.Writer = &bytes.Buffer{}
				if failed {
					output = serverErrorWriter{err: io.ErrClosedPipe}
				}
				executor := requestExecutor{
					provider: provider, output: output, mekugiCalls: proxy,
					serviceTiers: &serviceTierSettings{configured: map[string]string{"gpt-6-sol": "fast"}},
				}
				err = executor.execute(t.Context(), t.Context(), request, headers, "compact")
				if (err != nil) != failed {
					t.Fatalf("delivery: %v, want failure=%t", err, failed)
				}
				if mode == "off" {
					if len(provider.forwarded) != 1 {
						t.Fatalf("fallback forwards=%d", len(provider.forwarded))
					}
					forwarded, err := parseResponsesRequest(provider.forwarded[0])
					if err != nil || forwarded.model() != "gpt-6-sol" || jsonString(forwarded.fields, "service_tier") != "priority" {
						t.Fatalf("routing policy not preserved: %s (%v)", provider.forwarded[0], err)
					}
				} else if len(provider.forwarded) != 0 {
					t.Fatal("router compaction unexpectedly invoked the provider")
				}
			})
		}
	}
}

func TestForwardedCompactionKeepsRequestProjections(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	request, headers := journalCompactionRequest(t, workspace, transform.shellThreadID)
	// The shared fixture presets access programs and has no markers, which
	// would hide a compaction path that skipped request-wide projection.
	delete(request.fields, "access_programs")
	request.fields["input"] = mustTestJSON(t, []any{
		map[string]any{"role": "developer", "content": "keep" + instructionOmitStart + "use rtk" + instructionOmitEnd},
		map[string]any{"role": "user", "content": "Summarize."},
	})
	wire, err := journalCompactionSSE("upstream", "gpt-test", "Provider summary")
	if err != nil {
		t.Fatal(err)
	}
	response := serverHTTPResponse(string(wire))
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, io.Discard, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("forwards=%d", len(provider.forwarded))
	}
	forwarded, err := parseResponsesRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	if input := forwarded.fields["input"]; bytes.Contains(input, []byte("use rtk")) || !bytes.Contains(input, []byte("keep")) {
		t.Fatalf("omission markers were not stripped: %s", input)
	}
	if programs := forwarded.fields["access_programs"]; !bytes.Contains(programs, []byte(`"standard"`)) {
		t.Fatalf("standard cyber access was not projected: %s", programs)
	}
	if tools := forwarded.fields["tools"]; len(tools) != 0 && string(tools) != "[]" {
		t.Fatalf("compaction gained tools: %s", tools)
	}
}
