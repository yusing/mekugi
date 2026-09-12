package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func featureDebugOutput(t *testing.T) *debugOutput {
	t.Helper()
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	d, err := openDebugOutput(flags)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close(); _ = os.RemoveAll(filepath.Dir(d.paths[0])) })
	return d
}

func readFeatureUsage(t *testing.T, d *debugOutput) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(d.log.Name())
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] != "feature_usage" {
			continue
		}
		if event["schema_version"] != float64(1) {
			t.Fatalf("unexpected event: %v", event)
		}
		events = append(events, event)
	}
	return events
}

func TestFeatureUsageAllowlistAndDisabledLogging(t *testing.T) {
	var disabled featureUsageTrace
	disabled.record("commentary", "shell", "publication", "accepted", "", "")
	d := featureDebugOutput(t)
	trace := featureUsageTrace{debug: d, requestID: "request-1", threadID: "private\ntext", sessionID: strings.Repeat("s", 257)}
	for _, categories := range [][4]string{
		{"private text", "shell", "publication", "accepted"},
		{"commentary", "private text", "publication", "accepted"},
		{"commentary", "shell", "private text", "accepted"},
		{"commentary", "shell", "publication", "private text"},
		{"commentary", "code_mode", "lowering", "prepared"},
		{"commentary", "code_mode", "lowering", "unavailable"},
		{"commentary", "tool_field", "publication", "accepted"},
	} {
		trace.record(categories[0], categories[1], categories[2], categories[3], "", "")
	}
	if got := readFeatureUsage(t, d); len(got) != 0 {
		t.Fatalf("unrecognized categories retained: %v", got)
	}
	trace.record("commentary", "shell", "publication", "accepted", "call-1", "https://private.example/secret")
	got := readFeatureUsage(t, d)
	if len(got) != 1 || got[0]["request_id"] != "request-1" || got[0]["call_id"] != "call-1" {
		t.Fatalf("correlation lost: %v", got)
	}
	for _, key := range []string{"thread_id", "session_id", "message_id"} {
		if _, exists := got[0][key]; exists {
			t.Fatalf("unsafe identity retained: %s", key)
		}
	}
}

func TestFeatureUsageStructuredJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, arguments string
			eligible        bool
			want            int
		}{
			{"absent", `{}`, true, 0},
			{"empty", `{"journal":[]}`, true, 0},
			{"authored", `{"journal":[{"op":"add","text":"private authored text","report_now":true}]}`, true, 2},
			{"unowned", `{"journal":[{"op":"add","text":"private authored text","report_now":true}]}`, false, 0},
		} {
			t.Run(tc.name+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				d := featureDebugOutput(t)
				transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
				transform.featureTrace = featureUsageTrace{debug: d, requestID: "request-1", threadID: "thread-1"}
				if tc.eligible {
					transform.commentaryTools = commentaryToolCatalog{functionToolKey("", "lookup"): {qualifiedName: "lookup"}}
				}
				call := map[string]any{"type": "function_call", "name": "lookup", "call_id": "call-1", "id": "item-1", "arguments": tc.arguments}
				if stream {
					for _, event := range []map[string]any{
						{"type": "response.output_item.done", "item": call},
						{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{call}}},
					} {
						events, err := transform.TransformSSE(mustTestJSON(t, event))
						if err != nil {
							t.Fatal(err)
						}
						for _, payload := range events {
							transform.Delivered(payload)
						}
						transform.ReleaseDelivery()
					}
				} else {
					for range 2 {
						payload, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
						if err != nil {
							t.Fatal(err)
						}
						transform.Delivered(payload)
						transform.ReleaseDelivery()
					}
				}
				got := readFeatureUsage(t, d)
				if len(got) != tc.want {
					t.Fatalf("events = %v", got)
				}
				if tc.want != 0 {
					if got[0]["source"] != "tool_field" || got[0]["stage"] != "mutation" || got[0]["outcome"] != "accepted" || got[0]["call_id"] != "call-1" ||
						got[1]["source"] != "report_now" || got[1]["stage"] != "render" || got[1]["outcome"] != "prepared" {
						t.Fatalf("wrong stages: %v", got)
					}
					for _, event := range got {
						if event["feature"] != "journal" || event["request_id"] != "request-1" {
							t.Fatalf("wrong correlation: %v", event)
						}
					}
				}
				data, _ := os.ReadFile(d.log.Name())
				if bytes.Contains(data, []byte("private authored text")) {
					t.Fatal("journal text leaked into feature evidence")
				}
			})
		}
	}
}

func TestFeatureUsageRuntimePublicationAndRendering(t *testing.T) {
	for _, source := range []string{"shell", "code_mode"} {
		for _, stream := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				d := featureDebugOutput(t)
				transform, proxy := newRuntimeCommentaryTransform(t)
				transform.featureTrace = featureUsageTrace{debug: d, requestID: "request-1", threadID: transform.shellThreadID}
				proxy.commentary.debug = d
				var token string
				if source == "code_mode" {
					if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": shellCommentaryTestItem()})); err != nil {
						t.Fatal(err)
					}
					token = runtimeCommentaryToken(t, transform)
				} else {
					token = proxy.commentary.subscribeThread(transform.historySessionID, transform.shellThreadID, "")
				}
				server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
				t.Cleanup(server.Close)
				sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
				if err := sink.Publish(t.Context(), `{"op":"add","text":"private runtime text","report_now":true}`); err != nil {
					t.Fatal(err)
				}
				if err := sink.Publish(t.Context(), `{"op":"add","text":"  "}`); err == nil {
					t.Fatal("blank milestone accepted")
				}
				if err := sink.Complete(t.Context()); err != nil {
					t.Fatal(err)
				}
				var visible []byte
				if stream {
					events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}}}))
					if err != nil {
						t.Fatal(err)
					}
					visible = bytes.Join(events, nil)
				} else {
					var err error
					visible, err = transform.TransformJSON([]byte(`{"status":"completed","output":[]}`))
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Contains(visible, []byte("private runtime text")) {
					t.Fatal("telemetry changed journal delivery")
				}
				got := readFeatureUsage(t, d)
				if source == "code_mode" {
					if len(got) == 0 || got[0]["stage"] != "lowering" {
						t.Fatalf("missing lowering: %v", got)
					}
					got = got[1:]
				}
				if len(got) != 2 || got[0]["source"] != source || got[0]["stage"] != "mutation" || got[0]["outcome"] != "accepted" || got[1]["stage"] != "render" || got[1]["outcome"] != "prepared" {
					t.Fatalf("mutation/render evidence: %v", got)
				}
				for _, key := range []string{"request_id", "session_id"} {
					if _, exists := got[0][key]; exists {
						t.Fatalf("runtime publication invented %s", key)
					}
				}
				if got[0]["thread_id"] != transform.shellThreadID {
					t.Fatal("runtime thread identity lost")
				}
			})
		}
	}
}

func TestFeatureUsageConcurrentPublications(t *testing.T) {
	d := featureDebugOutput(t)
	broker := newCommentaryBroker()
	broker.debug = d
	t.Cleanup(broker.close)
	token := broker.subscribeThread("session", "thread", "")
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			if !broker.publish(token, "progress", false) {
				t.Error("publication rejected")
			}
		})
	}
	workers.Wait()
	events := readFeatureUsage(t, d)
	if len(events) != 16 {
		t.Fatalf("lost concurrent observations: %d", len(events))
	}
	seen := make(map[any]bool)
	for _, event := range events {
		if event["outcome"] != "accepted" || seen[event["message_id"]] {
			t.Fatalf("duplicate or rejected publication: %v", event)
		}
		seen[event["message_id"]] = true
	}
}

func TestFeatureUsageDoesNotInferRuntimeExecution(t *testing.T) {
	d := featureDebugOutput(t)
	transform, _ := newRuntimeCommentaryTransform(t)
	transform.featureTrace = featureUsageTrace{debug: d}
	for _, input := range []string{
		`text("await commentary('quoted')")`,
		`// await commentary("comment")`,
		`await something.commentary("property")`,
		`const broken = ; await commentary("unparsed")`,
	} {
		if _, changed, err := transform.lowerCodeModeCommentary("call-ignored", input); err != nil || changed {
			t.Fatalf("non-call was instrumented: %q, %v", input, err)
		}
	}
	// A provider message is authored evidence, even with a generated-looking ID,
	// but never proves runtime publication or router origin.
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed", "output": []any{assistantCommentaryMessage(commentaryMessageID("automatic"), "progress")},
	})); err != nil {
		t.Fatal(err)
	}
	if events := readFeatureUsage(t, d); len(events) != 1 || events[0]["source"] != "provider_message" || events[0]["stage"] != "authored" {
		t.Fatalf("provider origin was not distinguished from runtime execution: %v", events)
	}
	if _, changed, err := transform.lowerCodeModeCommentary("call-syntax", `await journal({op: "add", text: "one"}); await journal({op: "add", text: "two"});`); err != nil || !changed {
		t.Fatalf("lowering failed: %v", err)
	}
	got := readFeatureUsage(t, d)
	if len(got) != 2 || got[1]["stage"] != "lowering" || got[1]["outcome"] != "prepared" {
		t.Fatalf("lowering inferred runtime execution or counted expressions: %v", got)
	}
}
func TestFeatureUsageSuppressionAndWriteFailure(t *testing.T) {
	d := featureDebugOutput(t)
	transform, proxy := newRuntimeCommentaryTransform(t)
	transform.featureTrace = featureUsageTrace{debug: d}
	proxy.commentary.debug = d
	token := proxy.commentary.subscribeThread(transform.historySessionID, transform.shellThreadID, "")
	if !proxy.commentary.publish(token, strings.Repeat("x", maxCommentaryPublicationBytes+1), false) {
		t.Fatal("oversized publication changed authentication")
	}
	for range maxCommentaryEventsPerRoute + 1 {
		proxy.commentary.publish(token, "progress", false)
	}
	if proxy.commentary.publish("not-a-capability", "progress", false) {
		t.Fatal("unauthenticated publication accepted")
	}
	if transform.runtimeCommentaryMessage(publishedCommentary{messageID: "unknown", text: "progress"}) != nil {
		t.Fatal("unknown provenance rendered")
	}
	got := readFeatureUsage(t, d)
	if len(got) != maxCommentaryEventsPerRoute+3 || got[0]["outcome"] != "oversized" ||
		got[len(got)-2]["outcome"] != "capacity" || got[len(got)-1]["outcome"] != "suppressed" {
		t.Fatalf("suppression evidence = %v", got)
	}
	proxy.commentary.close()
	if _, changed, err := transform.lowerCodeModeCommentary("call-unavailable", `await journal({op: "add", text: "progress"})`); err == nil || changed {
		t.Fatalf("unavailable publisher changed lowering: %v", err)
	}
	got = readFeatureUsage(t, d)
	if got[len(got)-1]["outcome"] != "unavailable" {
		t.Fatal("missing publisher-unavailable evidence")
	}

	if err := d.log.Close(); err != nil {
		t.Fatal(err)
	}
	transform.commentaryTools = commentaryToolCatalog{functionToolKey("", "lookup"): {qualifiedName: "lookup"}}
	if _, err := transform.TransformJSON([]byte(`{"status":"completed","output":[{"type":"function_call","name":"lookup","call_id":"call-failure","arguments":"{\"journal\":[{\"op\":\"add\",\"text\":\"progress\",\"report_now\":true}]}"}]}`)); err != nil {
		t.Fatalf("debug write failure affected tool response: %v", err)
	}
	if d.err == nil {
		t.Fatal("debug write failure was not retained for shutdown")
	}
}

func TestFeatureUsageProductionRequestCorrelation(t *testing.T) {
	d := featureDebugOutput(t)
	dump, err := os.Create(filepath.Join(t.TempDir(), "instructions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	d.dump = dump
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
	initial := serverRequest(t, func(fields map[string]any) {
		fields["tools"] = []any{map[string]any{
			"type": "function", "name": "lookup",
			"parameters": map[string]any{"type": "object"},
		}}
	})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"function_call","name":"lookup","call_id":"call-production","arguments":"{\"journal\":[{\"op\":\"add\",\"text\":\"private progress\",\"report_now\":true}]}"}]}`)),
	}}}}
	handler := d.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := executeRequest(r.Context(), r.Context(), initial, headers, "public-session", provider, w, nil, proxy, nil, nil); err != nil {
			t.Error(err)
		}
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if !bytes.Contains(response.Body.Bytes(), []byte("private progress")) {
		t.Fatal("request did not render authored commentary")
	}
	events := readFeatureUsage(t, d)
	if len(events) != 2 {
		t.Fatalf("request did not produce feature evidence: %v", events)
	}
	data, err := os.ReadFile(d.log.Name())
	if err != nil {
		t.Fatal(err)
	}
	var requestID string
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] == "request_complete" {
			requestID, _ = event["request_id"].(string)
		}
	}
	for _, event := range events {
		if requestID == "" || event["request_id"] != requestID ||
			event["session_id"] != "public-session" || event["thread_id"] != codexThreadID(headers) {
			t.Fatalf("feature/request correlation mismatch: %v", event)
		}
	}
	if bytes.Contains(data, []byte("private progress")) {
		t.Fatal("request feature evidence leaked commentary text")
	}
}
