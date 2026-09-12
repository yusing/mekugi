package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDebugSessionArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var paths []string
	err := RunSession(ctx, []string{"--debug", "--mode", "passthrough"}, nil, func(session Session) {
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
	if !bytes.Contains(log, []byte(`"feature_usage_schema":1`)) ||
		!bytes.Contains(log, []byte(`"feature_usage_features":["journal","commentary"]`)) {
		t.Fatal("missing feature-observation coverage marker")
	}
	metrics, _ := os.ReadFile(paths[2])
	if !json.Valid(metrics) {
		t.Fatal("missing final metrics")
	}
}

func TestDebugInstructionSelectionAndWriteFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	d, err := openDebugOutput(flags)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"test","instructions":"exact\n  text\t","input":[{"role":"developer","content":[{"type":"input_text","text":"inherited"}]},{"type":"additional_tools","tools":[{"name":"shell","description":"exact tool"}]},{"role":"user","content":"excluded user"},{"type":"custom_tool_call","input":"excluded script"}]}`)
	headers := http.Header{"Authorization": {"excluded credential"}, "X-Client-Request-Id": {"client-1"}}
	d.instructions(body, body, headers, "session-1", "request-1", 1)
	dump, _ := os.ReadFile(d.paths[3])
	var got struct {
		Instructions   string            `json:"instructions"`
		Developers     []json.RawMessage `json:"developer_messages"`
		Additional     []json.RawMessage `json:"additional_tools"`
		Cached         int               `json:"cached_input_items"`
		Scope          string            `json:"scope"`
		WireDevelopers []json.RawMessage `json:"wire_developer_messages"`
	}
	if err := json.Unmarshal(dump, &got); err != nil || got.Instructions != "exact\n  text\t" || len(got.Developers) != 1 || len(got.Additional) != 1 || got.Cached != 1 {
		t.Fatalf("instruction selection: %+v, %v", got, err)
	}
	if bytes.Contains(dump, []byte("excluded")) {
		t.Fatal("dump retained data outside instruction scope")
	}
	if got.Scope != "projected_responses_request" || !sameJSONValue(mustMarshalJSON(got.WireDevelopers), mustMarshalJSON(got.Developers)) {
		t.Fatal("dump does not distinguish projection from wire instructions")
	}
	// Post-startup write failure is retained for shutdown, not returned into
	// request execution or printed over the active Codex terminal.
	if err := d.dump.Close(); err != nil {
		t.Fatal(err)
	}
	d.instructions(body, body, headers, "session-1", "request-2", 0)
	if err := d.close(); err == nil {
		t.Fatal("debug write failure was lost")
	}
}

func TestDebugStartupFailureReportsArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing", "capture.jsonl")
	var paths []string
	err := RunSession(t.Context(), []string{"--debug", "--mode", "passthrough", "--capture-output", missing}, nil, func(Session) {
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
	err := RunSession(ctx, []string{"--debug", "--mode", "passthrough"}, nil, func(Session) {
		t.Error("canceled startup reached readiness")
	}, func(artifacts []string) { paths = artifacts })
	if err != nil || len(paths) != 6 {
		t.Fatalf("canceled startup lost paths: %v, %v", paths, err)
	}
}

func TestDebugWebSocketInheritedInstructions(t *testing.T) {
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	d, err := openDebugOutput(flags)
	if err != nil {
		t.Fatal(err)
	}
	// The plugin proxy fixture is shared until TestMain exits, so do not put
	// its directory under this test's temporary TMPDIR.
	t.Cleanup(func() { _ = d.close(); _ = os.RemoveAll(filepath.Dir(d.paths[0])) })
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
					if jsonString(item, "role") == "developer" {
						firstDevelopers = append(firstDevelopers, raw)
					}
				}
			} else if len(input) != 1 || jsonString(request, "previous_response_id") != "first" {
				t.Error("continuation did not use upstream cached input")
			}
			dump, err := os.ReadFile(d.paths[3])
			if err != nil {
				t.Error(err)
				return
			}
			lines := bytes.Split(bytes.TrimSpace(dump), []byte{'\n'})
			var record struct {
				Instructions   json.RawMessage   `json:"instructions"`
				Developers     []json.RawMessage `json:"developer_messages"`
				Cached         int               `json:"cached_input_items"`
				WireDevelopers []json.RawMessage `json:"wire_developer_messages"`
				WireAdditional []json.RawMessage `json:"wire_additional_tools"`
				WireParent     string            `json:"wire_previous_response_id"`
			}
			if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil {
				t.Error(err)
				return
			}
			if !sameJSONValue(record.Instructions, request["instructions"]) || len(record.Developers) == 0 || !sameJSONValue(mustMarshalJSON(record.Developers), mustMarshalJSON(firstDevelopers)) {
				t.Errorf("dump lost exact final or inherited instructions: top-level equal=%v, dump developer count=%d, wire count=%d", sameJSONValue(record.Instructions, request["instructions"]), len(record.Developers), len(firstDevelopers))
			}
			if id == "second" && record.Cached == 0 {
				t.Error("dump lost cached-prefix provenance")
			}
			if id == "second" && (len(record.WireDevelopers) != 0 || len(record.WireAdditional) != 0 || record.WireParent != "first") {
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
	proxy := newToolPluginTestProxy(t)
	proxy.customizedInstructions = true
	endpoint := responsesWebSocketHandler(ctx, 10*time.Second, newProviderClient(upstream.URL, upstream.Client()), nil, proxy, mustCTP2Codec(t), nil)
	t.Cleanup(endpoint.Close)
	server := httptest.NewServer(d.handler(endpoint))
	t.Cleanup(server.Close)
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "debug-session")
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
			if jsonString(event, "type") == "error" {
				t.Fatalf("request failed: %s", mustMarshalJSON(event))
			}
			if jsonString(event, "type") == "response.completed" {
				break
			}
		}
	}
}
