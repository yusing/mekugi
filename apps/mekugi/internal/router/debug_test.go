package router

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessioninspection/query"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestDebugSessionArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var paths []string
	err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--debug", "--mode", "passthrough"}, nil, func(session Session) {
		if session.AXReadOutput == "" {
			t.Error("debug did not enable runtime AX reads")
		}
		cancel()
	}, func(artifacts []string) { paths = artifacts })
	if err != nil || len(paths) != 6 {
		t.Fatalf("debug session: %v, paths %v", err, paths)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !filepath.IsAbs(path) || info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %s: %v, %v", path, info, err)
		}
	}
	log, _ := os.ReadFile(paths[0])
	if !bytes.Contains(log, []byte("router_start")) || !bytes.Contains(log, []byte("router_stop")) {
		t.Fatal("missing lifecycle diagnostics")
	}
	metrics, _ := os.ReadFile(paths[2])
	var exported struct {
		StorageWrites *struct {
			Bytes uint64 `json:"bytes"`
			Scope string `json:"scope"`
		} `json:"storage_writes"`
	}
	if err := json.Unmarshal(metrics, &exported); err != nil || exported.StorageWrites == nil || exported.StorageWrites.Bytes == 0 || exported.StorageWrites.Scope == "" {
		t.Fatalf("missing application write accounting: %s, %v", metrics, err)
	}
	root, err := debugStorageDirectory()
	if err != nil || filepath.Dir(filepath.Dir(paths[0])) != root {
		t.Fatalf("bundle not in managed state: %v, %v", paths, err)
	}
}

func TestDebugCustomCaptureBundleSupportsReplay(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	custom := filepath.Join(t.TempDir(), "custom capture.jsonl")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var paths []string
	if err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--debug", "--mode", "passthrough", "--capture-output", custom}, nil,
		func(Session) { cancel() }, func(artifacts []string) { paths = artifacts }); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 6 || paths[1] != custom {
		t.Fatalf("selected capture destination changed: %q", paths)
	}
	debugDir := filepath.Dir(paths[0])
	bundle, err := os.Stat(filepath.Join(debugDir, "capture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := os.Stat(custom)
	if err != nil || !os.SameFile(bundle, selected) {
		t.Fatalf("debug capture does not resolve to the selected file: %v", err)
	}
	// Representative retained provider evidence in the real producer's chosen
	// capture destination must reach the existing replay consumer.
	replayTestWrite(t, filepath.Dir(custom), filepath.Base(custom), map[string]any{
		"boundary": "provider", "thread_id": "root", "captured_at": time.UnixMilli(replayTestEpoch + 50), "duration_ms": 20,
	})
	rollout := replayTestWrite(t, t.TempDir(), "rollout-root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1, replayTestEpoch+2, map[string]any{
			"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "Replay complete."}},
		}))
	replay, err := replayReadTest(t, new(transportcapture.Service), t.Context(), rollout, debugDir, 1)
	if err != nil || replay == nil || replay.Providers != 1 {
		t.Fatalf("custom capture replay: %v, error=%v", replay, err)
	}
}

func TestDebugStartupFailureReportsArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing", "capture.jsonl")
	var paths []string
	err := testRunSession(testHistory(), testRecovery(), t.Context(), testCommands(), []string{"--debug", "--mode", "passthrough", "--capture-output", missing}, nil, func(Session) {
		t.Error("ready despite capture initialization failure")
	}, func(artifacts []string) { paths = artifacts })
	if err == nil || len(paths) != 6 {
		t.Fatalf("startup failure lost artifact paths: %v", err)
	}
}

func TestDebugCanceledStartupReportsArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var paths []string
	err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--debug", "--mode", "passthrough"}, nil, func(Session) {
		t.Error("canceled startup reached readiness")
	}, func(artifacts []string) { paths = artifacts })
	if err != nil || len(paths) != 6 {
		t.Fatalf("canceled startup lost paths: %v, %v", paths, err)
	}
}

func TestDebugWebSocketInheritedInstructions(t *testing.T) {
	t.Parallel()
	d, err := openDebugOutput(testDiagnostics(new(evidence.Service), new(transportcapture.Service)), true, "")
	if err != nil {
		t.Fatal(err)
	}
	// The plugin proxy fixture is shared until TestMain exits, so do not put
	// its directory under this test's temporary TMPDIR.
	t.Cleanup(func() { _ = d.Close(); _ = os.RemoveAll(filepath.Dir(d.Paths()[0])) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var firstDevelopers []json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for _, id := range []string{"first", "second"} {
			request, err := providerSocketRead(ctx, conn)
			if err != nil {
				t.Error(err)
				return
			}
			var input []json.RawMessage
			_ = json.Unmarshal(request["input"], &input)
			if id == "first" {
				for _, raw := range input {
					var item map[string]json.RawMessage
					_ = json.Unmarshal(raw, &item)
					if responses.StringField(item, "role") == "developer" {
						firstDevelopers = append(firstDevelopers, raw)
					}
				}
			} else if len(input) != 1 || responses.StringField(request, "previous_response_id") != "first" {
				t.Error("continuation did not use upstream cached input")
			}
			data, err := os.ReadFile(d.Paths()[3])
			if err != nil {
				t.Error(err)
				return
			}
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			var record struct {
				RequestID      string `json:"request_id"`
				Cached         int    `json:"cached_input_items"`
				WireParent     string `json:"wire_previous_response_id"`
				ProjectedBytes int    `json:"projected_input_bytes"`
				WireBytes      int    `json:"wire_input_bytes"`
			}
			if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil {
				t.Error(err)
				return
			}
			report, failures := testDiagnostics(nil, nil).InspectBundle(ctx, diagnostic.InspectOptions{Directory: filepath.Dir(d.Paths()[3]), RequestID: record.RequestID, Field: "all", Limit: 50, TextBytes: 65536})
			for _, failure := range failures {
				if failure.Path == d.Paths()[3] {
					t.Error(failure.Err)
					return
				}
			}
			var text map[string]query.Text
			for _, entry := range report.Evidence {
				if entry.Source == "instructions.jsonl" {
					text = entry.Text
				}
			}
			var developers, wireDevelopers, wireAdditional []json.RawMessage
			for key, target := range map[string]*[]json.RawMessage{"developer_messages": &developers, "wire_developer_messages": &wireDevelopers, "wire_additional_tools": &wireAdditional} {
				if err := json.Unmarshal([]byte(text[key].Text), target); err != nil {
					t.Error(err)
					return
				}
			}
			if text["instructions"].Text != responses.StringField(request, "instructions") || len(developers) == 0 || !responses.SameJSONValue(responses.MustMarshalJSON(developers), responses.MustMarshalJSON(firstDevelopers)) {
				t.Error("dump lost exact final or inherited instructions")
			}
			if record.WireBytes != len(request["input"]) || record.ProjectedBytes < record.WireBytes || (id == "second" && record.ProjectedBytes == record.WireBytes) {
				t.Errorf("incorrect projected/wire byte evidence: %d/%d", record.ProjectedBytes, record.WireBytes)
			}
			if id == "second" && record.Cached == 0 {
				t.Error("dump lost cached-prefix provenance")
			}
			if id == "second" && (len(wireDevelopers) != 0 || len(wireAdditional) != 0 || record.WireParent != "first") {
				t.Error("dump claims cached instructions were sent on continuation")
			}
			if err := providerSocketWrite(ctx, conn, socketEvent("response.created", id)); err != nil {
				t.Error(err)
				return
			}
			if err := providerSocketWrite(ctx, conn, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	}))
	t.Cleanup(upstream.Close)
	proxy := newManagedMekugiProxy(t)
	endpoint := testWebSocketEndpoint(ctx, 10*time.Second, newTestProviderClient(upstream.URL, upstream.Client()), nil, proxy)
	t.Cleanup(endpoint.Close)
	server := httptest.NewServer(d.Handler(endpoint))
	t.Cleanup(server.Close)
	headers := codexAuthHeaders()
	headers.Set(codexresponses.RoutingSessionHeader, "debug-session")
	maps.Copy(headers, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil}))
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	for _, id := range []string{"first", "second"} {
		request := map[string]any{"type": "response.create", "model": "gpt-test", "instructions": "Follow the task.", "tools": []any{map[string]string{"type": "function", "name": "lookup"}}}
		if id == "first" {
			request["input"] = []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"role": "developer", "content": "Preserve\n  exact spacing."}, map[string]string{"role": "user", "content": "first task"}}
		} else {
			request["previous_response_id"] = "first"
			request["input"] = []any{map[string]string{"role": "user", "content": "next task"}}
		}
		socketWrite(t, ctx, conn, request)
		for {
			event := socketRead(t, ctx, conn)
			if responses.StringField(event, "type") == "error" {
				t.Fatalf("request failed: %s", responses.MustMarshalJSON(event))
			}
			if responses.StringField(event, "type") == "response.completed" {
				break
			}
		}
	}
}
