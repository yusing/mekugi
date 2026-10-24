package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestApprovalReviewRestoresCompactionAtProvider(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			driver, err := threadbinding.NewDriver(control.New())
			if err != nil {
				t.Fatal(err)
			}
			proxy.threadControl = driver
			ref, err := proxy.StoreCompact(ctx, "parent", "exact retained handover", `{"sources":[{"path":"must-not-be-read.txt"}]}`)
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]any{
				"model": "gpt-test", "instructions": "Native approval policy.",
				"tools": testExecResponsesTools(), "tool_choice": "auto",
				"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "review", "schema": map[string]any{"type": "object"}}},
				"input": []any{
					map[string]any{"type": "compaction", "id": "cmp-local", "encrypted_content": ref},
					map[string]any{"type": "compaction_summary", "encrypted_content": ref},
					map[string]any{"type": "compaction", "id": "cmp-provider", "encrypted_content": "provider-opaque"},
					map[string]any{"role": "user", "content": "Review this action."},
				},
			}
			headers := http.Header{"Authorization": {"Bearer fixture"}}
			headers.Set("ChatGPT-Account-ID", "fixture")
			headers.Set(codexresponses.ThreadIDHeader, "reviewer")
			headers.Set(codexresponses.GuardianHeader, "reviewer")
			headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, map[string]any{
				"thread_id": "reviewer", "parent_thread_id": "parent", "subagent_kind": "guardian", "request_kind": "turn", "turn_id": "review-turn",
			})))
			forwarded := make(chan []byte, 1)
			const response = `{"id":"review-result","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"denied"}]}]}`
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if transport == "websocket" {
					conn, e := websocket.Accept(w, r, nil)
					if e != nil {
						return
					}
					defer conn.CloseNow()
					conn.SetReadLimit(codexresponses.MaxRequestBytes)
					_, body, e := conn.Read(ctx)
					if e != nil {
						return
					}
					forwarded <- body
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.completed","response":`+response+`}`))
					return
				}
				body, _ := io.ReadAll(r.Body)
				forwarded <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, response)
			}))
			defer provider.Close()
			client := inferencegateway.NewClient(provider.URL, provider.Client(), requestexecution.ProviderDiagnostics())
			executor := testRequestExecutor(client, nil, proxy)
			var output []byte
			if transport == "websocket" {
				endpoint := inferencegateway.NewWebSocketEndpoint(ctx, 5*time.Second, client, executor.ExecuteWebSocket)
				defer endpoint.Close()
				router := httptest.NewServer(endpoint)
				defer router.Close()
				conn, _, e := websocket.Dial(ctx, router.URL, &websocket.DialOptions{HTTPHeader: headers})
				if e != nil {
					t.Fatal(e)
				}
				defer conn.CloseNow()
				fields["type"] = "response.create"
				if e = conn.Write(ctx, websocket.MessageText, mustTestJSON(t, fields)); e != nil {
					t.Fatal(e)
				}
				_, output, err = conn.Read(ctx)
			} else {
				request, e := codexresponses.ParseRequest(mustTestJSON(t, fields))
				if e != nil {
					t.Fatal(e)
				}
				var buffer bytes.Buffer
				err = executor.ExecuteHTTP(ctx, ctx, request, headers, &buffer)
				output = buffer.Bytes()
			}
			if err != nil {
				t.Fatal(err)
			}
			var body []byte
			select {
			case body = <-forwarded:
			case <-ctx.Done():
				t.Fatal("review did not reach provider")
			}
			var got map[string]jsontext.Value
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"instructions", "tools", "tool_choice", "text"} {
				if !bytes.Equal(got[key], mustTestJSON(t, fields[key])) {
					t.Fatalf("reviewer %s changed", key)
				}
			}
			var input []map[string]jsontext.Value
			if err := json.Unmarshal(got["input"], &input); err != nil {
				t.Fatal(err)
			}
			if len(input) != 4 || bytes.Contains(got["input"], []byte(ref)) || bytes.Count(got["input"], []byte("exact retained handover")) != 2 {
				t.Fatalf("provider received unresolved local compaction instead of both exact summaries: %s", got["input"])
			}
			if string(input[2]["encrypted_content"]) != `"provider-opaque"` || string(input[3]["content"]) != `"Review this action."` || bytes.Contains(body, []byte("must-not-be-read.txt")) {
				t.Fatal("native history changed or reload metadata leaked")
			}
			if !bytes.Contains(output, []byte(`"text":"denied"`)) {
				t.Fatalf("native decision changed: %s", output)
			}
			if _, admitted := driver.Coordination().Member("reviewer"); admitted {
				t.Fatal("reviewer entered managed-agent tree")
			}
			if _, found, err := proxy.retained.Store.ReadScope("reviewer"); found || err != nil {
				t.Fatal("reviewer published retained scope", err)
			}
		})
	}
}

func TestApprovalReviewPreservesNativeRequestAndChildAdmission(t *testing.T) {
	for _, kind := range []string{"guardian", "internal", "thread_spawn"} {
		t.Run(kind, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			driver, err := threadbinding.NewDriver(control.New())
			if err != nil {
				t.Fatal(err)
			}
			proxy.threadControl = driver
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "instructions": "Review the proposed action under the approval policy.",
				"input": []any{map[string]any{"role": "user", "content": "Review this action."}, map[string]any{"type": "compaction", "encrypted_content": "provider-opaque"}},
				"tools": testExecResponsesTools(), "tool_choice": "auto",
				"access_programs": map[string]string{"cyber": "standard"},
				"text":            map[string]any{"format": map[string]any{"type": "json_schema", "name": "review", "schema": map[string]any{"type": "object"}}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			original, err := request.WireBody(request.Fields)
			if err != nil {
				t.Fatal(err)
			}
			headers := make(http.Header)
			headers.Set(codexresponses.ThreadIDHeader, "review-thread")
			headers.Set(codexresponses.RoutingSessionHeader, "review-session")
			metadata := map[string]any{"thread_id": "review-thread", "turn_id": "review-turn", "request_kind": "turn", "workspaces": map[string]any{t.TempDir(): nil}}
			if kind == "internal" {
				headers.Set(codexresponses.GuardianHeader, "reviewer")
				headers.Set(codexresponses.SubagentHeader, "guardian")
			} else {
				metadata["subagent_kind"] = kind
			}
			headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
			const response = `{"id":"review-result","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"denied"}]}]}`
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(response)}}}
			executor := testRequestExecutor(provider, nil, proxy)
			var output bytes.Buffer
			err = executor.ExecuteHTTP(t.Context(), t.Context(), request, headers, &output)
			if kind == "thread_spawn" {
				if err == nil || !strings.Contains(err.Error(), "independent child request preceded logical admission") || len(provider.forwarded) != 0 {
					t.Fatalf("unadmitted child escaped admission: %v, forwards=%d", err, len(provider.forwarded))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) {
				t.Fatalf("native reviewer request changed: %s", provider.forwarded)
			}
			if !strings.Contains(output.String(), `"text":"denied"`) {
				t.Fatalf("review decision changed: %s", output.String())
			}
			if _, admitted := driver.Coordination().Member("review-thread"); admitted {
				t.Fatal("reviewer entered managed-agent tree")
			}
		})
	}
}

func TestApprovalReviewCompactionAuthority(t *testing.T) {
	for _, scenario := range []string{"parent", "internal_parent", "fork", "escaped", "retained_parent", "conflicting_parent", "retained_fork", "late_fork", "fork_parent_override", "foreign", "unknown", "missing", "corrupt", "no_metadata", "mismatched_thread", "invalid_parent", "self_parent"} {
		t.Run(scenario, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			ref, err := proxy.StoreCompact(t.Context(), "parent", "retained summary", "")
			if err != nil {
				t.Fatal(err)
			}
			metadata := map[string]any{"thread_id": "reviewer", "parent_thread_id": "parent", "subagent_kind": "guardian", "request_kind": "turn"}
			switch scenario {
			case "retained_parent", "conflicting_parent", "retained_fork", "late_fork", "fork_parent_override":
				scope := history.Scope{Thread: "reviewer", Namespace: "parent", Parent: "parent"}
				if strings.Contains(scenario, "fork") {
					scope = history.Scope{Thread: "reviewer", Namespace: "reviewer", Fork: "parent"}
					if scenario != "fork_parent_override" {
						delete(metadata, "parent_thread_id")
						delete(metadata, "subagent_kind")
						metadata["forked_from_thread_id"] = "parent"
					}
				}
				if err := proxy.retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
					return tx.InitializeScope(scope, nil)
				}); err != nil {
					t.Fatal(err)
				}
				if scenario == "conflicting_parent" || scenario == "late_fork" || scenario == "fork_parent_override" {
					source := "parent"
					if scenario == "conflicting_parent" {
						source = "unrelated"
						metadata["parent_thread_id"] = source
					}
					ref, err = proxy.StoreCompact(t.Context(), source, "retained summary", "")
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			priorScope, priorExists, err := proxy.retained.Store.ReadScope("reviewer")
			if err != nil {
				t.Fatal(err)
			}
			references := []string{ref}
			switch scenario {
			case "internal_parent":
				delete(metadata, "subagent_kind")
			case "fork":
				delete(metadata, "parent_thread_id")
				delete(metadata, "subagent_kind")
				metadata["forked_from_thread_id"] = "parent"
			case "foreign":
				metadata["parent_thread_id"] = "unrelated"
			case "unknown":
				// Validate every reference before publishing any projection.
				references = append(references, compact.Prefix+"parent:amber999")
			case "missing", "corrupt":
				path := filepath.Join(proxy.retained.Store.Directory(), compact.Name(ref))
				if scenario == "missing" {
					err = os.Remove(path)
				} else {
					err = os.WriteFile(path, []byte(`{"Version":1,"Reference":"wrong","Summary":"wrong"}`), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "mismatched_thread":
				metadata["thread_id"] = "other"
			case "invalid_parent":
				metadata["parent_thread_id"] = 42
			case "self_parent":
				metadata["parent_thread_id"] = "reviewer"
			}
			var items []any
			for _, reference := range references {
				items = append(items, map[string]any{"type": "compaction", "encrypted_content": reference})
			}
			body := mustTestJSON(t, map[string]any{"model": "gpt-test", "input": items})
			if scenario == "escaped" {
				body = bytes.ReplaceAll(body, []byte("mekugi:"), []byte(`mekugi\u003a`))
			}
			request, err := codexresponses.ParseRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(request.Fields["input"])
			headers := make(http.Header)
			headers.Set(codexresponses.ThreadIDHeader, "reviewer")
			headers.Set(codexresponses.GuardianHeader, "reviewer")
			if scenario != "no_metadata" {
				headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"review","status":"completed","output":[]}`)}}}
			err = testRequestExecutor(provider, nil, proxy).ExecuteHTTP(t.Context(), t.Context(), request, headers, io.Discard)
			success := scenario == "parent" || scenario == "internal_parent" || scenario == "fork" || scenario == "escaped" || scenario == "retained_parent" || scenario == "retained_fork"
			if success {
				if err != nil || len(provider.forwarded) != 1 || !bytes.Contains(provider.forwarded[0], []byte("retained summary")) {
					t.Fatalf("authorized recovery failed: %v", err)
				}
			} else if err == nil || len(provider.forwarded) != 0 || !bytes.Equal(request.Fields["input"], original) {
				t.Fatalf("invalid recovery forwarded or partially changed context: %v, forwards=%d", err, len(provider.forwarded))
			}
			if scope, found, err := proxy.retained.Store.ReadScope("reviewer"); found != priorExists || err != nil || !bytes.Equal(mustTestJSON(t, scope), mustTestJSON(t, priorScope)) {
				t.Fatal("reviewer changed retained scope", err)
			}
		})
	}
}
