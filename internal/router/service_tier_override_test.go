package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/coder/websocket"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotNativeConfiguredServiceTier(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mekugi", "config.toml"), []byte("[service_tiers]\n\"gpt-6.1-sol\" = \"fast\"\n\"gpt-6-astra\" = \"flex\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadMekugiConfig()
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, directory)
	u.proxy = newManagedMekugiProxy(t)
	u.serviceTiers = config.ServiceTiers
	u.status = "Ready"
	u.view.painter.Theme = livediff.DarkTheme
	for _, tc := range []struct{ model, requested, want string }{
		{"gpt-6.1-sol", "default", "priority"},
		{"gpt-6-astra", "priority", "flex"},
		{"unconfigured", "default", "default"},
		{"unconfigured", "fast", "priority"},
	} {
		t.Run(tc.model+"-"+tc.requested, func(t *testing.T) {
			appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "main", "threadSettings": map[string]any{"model": tc.model, "effort": "high", "serviceTier": tc.requested}})
			rows, _ := u.mainFrame(100, 10, 0)
			if frame := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(frame, tc.model+" (high) · "+tc.want) {
				t.Fatalf("composer does not reflect routed tier: %s", frame)
			}
			if tc.model == "gpt-6.1-sol" {
				assertNativeUISnapshot(t, "native-main-configured-tier", rows)
			}
			// Passthrough routes tiers too, but has no observation proxy.
			proxy := u.proxy
			u.proxy = nil
			passthrough, _ := u.mainFrame(100, 10, 0)
			if strings.Join(rows, "\n") != strings.Join(passthrough, "\n") {
				t.Fatal("passthrough composer lost configured tier")
			}
			appServerTestKeys(t, u, "/status\r")
			if body := statusReportText(u.statusPanel); !strings.Contains(body, "Service tier: "+tc.want) {
				t.Fatalf("status does not reflect routed tier: %s", body)
			}
			u.statusPanelKey("\x1b")
			u.proxy = proxy
			if u.serviceTier != tc.requested || u.model != tc.model {
				t.Fatal("presentation replaced host settings")
			}
			request := serverRequest(t, func(fields map[string]any) { fields["model"], fields["service_tier"] = tc.model, tc.requested })
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestExecutor{provider: provider, output: &bytes.Buffer{}, serviceTiers: config.ServiceTiers}
			if err := executor.execute(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := parseResponsesRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := jsonString(forwarded.fields, "service_tier"); got != tc.want {
				t.Fatalf("provider tier=%s, display=%s", got, tc.want)
			}
		})
	}
}

func TestServiceTierConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ value, want string }{
		{"fast", "priority"}, {"priority", "priority"}, {"default", "default"},
		{"auto", "auto"}, {"flex", "flex"}, {"", ""}, {"invalid", ""},
	} {
		t.Run(tc.value, func(t *testing.T) {
			body := fmt.Sprintf("[service_tiers]\n\"gpt-6-astra\" = %q\n", tc.value)
			if err := os.WriteFile(filepath.Join(directory, "mekugi", "config.toml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := loadMekugiConfig()
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid tier accepted")
				}
				return
			}
			if err != nil || config.ServiceTiers["gpt-6-astra"] != tc.want {
				t.Fatalf("config=%v err=%v", config.ServiceTiers, err)
			}
		})
	}
}

func TestServiceTierOverrideAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "sse", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			received := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var fields map[string]jsontext.Value
				if transport == "websocket" {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					_, body, err := conn.Read(ctx)
					if err != nil {
						t.Error(err)
						return
					}
					if err := json.Unmarshal(body, &fields); err != nil {
						t.Error(err)
						return
					}
					received <- string(fields["service_tier"])
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"tier-test","status":"completed","output":[]}}`))
					_, _, _ = conn.Read(ctx)
					return
				}
				if err := json.UnmarshalRead(r.Body, &fields); err != nil {
					t.Error(err)
					return
				}
				received <- string(fields["service_tier"])
				if transport == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"tier-test\",\"status\":\"completed\",\"output\":[]}}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"tier-test","status":"completed","output":[]}`)
				}
			}))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.serviceTiers = map[string]string{"gpt-6-astra": "priority"}
			request := serverRequest(t, func(fields map[string]any) {
				fields["model"], fields["service_tier"], fields["stream"] = "gpt-6-astra", "default", transport != "http"
			})
			if transport == "websocket" {
				endpoint := responsesWebSocketHandler(ctx, 10*time.Second, client, nil, nil)
				defer endpoint.Close()
				server := httptest.NewServer(endpoint)
				defer server.Close()
				conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				request.fields["type"] = mustMarshalJSON("response.create")
				socketWrite(t, ctx, conn, request.fields)
				event := socketRead(t, ctx, conn)
				if jsonString(event, "type") != "response.completed" {
					t.Fatalf("event=%s", event)
				}
			} else {
				req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(mustTestJSON(t, request.fields)))
				req.Header = codexAuthHeaders()
				recorder := httptest.NewRecorder()
				responsesHandler(ctx, 10*time.Second, client, nil, nil)(recorder, req)
				if recorder.Code != 200 {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
				}
			}
			select {
			case tier := <-received:
				if tier != `"priority"` {
					t.Fatalf("upstream tier=%s, want priority", tier)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestServiceTierOverrideUsesEffectiveModel(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-terra"} {
		t.Run(model, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			request := serverRequest(t, func(fields map[string]any) { fields["model"], fields["service_tier"] = model, "flex" })
			headers := serverMetadataHeaders(t, "turn", nil)
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestExecutor{provider: provider, output: &bytes.Buffer{}, mekugiCalls: proxy, serviceTiers: map[string]string{"gpt-6-sol": "fast"}}
			if err := executor.execute(t.Context(), t.Context(), request, headers, "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := parseResponsesRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			wantTier, wantModel := "flex", model
			if model == "gpt-5.6-terra" {
				wantTier, wantModel = "priority", "gpt-6-sol"
			}
			if got := jsonString(forwarded.fields, "service_tier"); got != wantTier {
				t.Fatalf("tier=%s want=%s", got, wantTier)
			}
			start := nativeSubagentStart(&forwarded)
			if start == nil || start.model != wantModel || start.tier != strings.ReplaceAll(wantTier, "priority", "fast") {
				t.Fatalf("start=%+v", start)
			}
		})
	}
}

func TestSubagentStartServiceTier(t *testing.T) {
	for _, tc := range []struct{ tier, want string }{
		{"priority", "fast"}, {"fast", "fast"}, {"default", "default"}, {"", ""},
	} {
		request := serverRequest(t, func(fields map[string]any) {
			fields["model"] = "gpt-6-astra"
			if tc.tier != "" {
				fields["service_tier"] = tc.tier
			}
		})
		got := nativeSubagentStart(&request)
		if got == nil || *got != (activityStart{model: "gpt-6-astra", effort: "high", tier: tc.want}) {
			t.Fatalf("tier=%s start=%+v", tc.tier, got)
		}
	}
}

func TestServiceTierSurvivesProviderTranslation(t *testing.T) {
	for _, service := range []openCodeService{{}, {prefix: "opencode-go"}, {prefix: "opencode-zen"}} {
		if service.prefix == "" {
			tr, err := translateChatRequest([]byte(`{"model":"grok:grok-4.6","service_tier":"fast","input":[]}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(mustTestJSON(t, tr.body["service_tier"])); got != `"fast"` {
				t.Fatalf("Grok tier=%s", got)
			}
			continue
		}
		formats := make(map[string]bool)
		for _, model := range service.models() {
			format := service.format(model.id)
			if formats[format] {
				continue
			}
			formats[format] = true
			t.Run(service.prefix+"/"+format, func(t *testing.T) {
				body := mustTestJSON(t, map[string]any{"model": service.prefix + ":" + model.id, "service_tier": "fast", "input": []any{}})
				tr, err := translateChatRequest(body, &service)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(mustTestJSON(t, tr.body["service_tier"])); got != `"fast"` {
					t.Fatalf("tier=%s", got)
				}
			})
		}
	}
}

func TestServiceTierJournalContinuationAndRootNotice(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
	proxy.activity.attachNativePane("root")
	defer root.Close()
	headers := serverMetadataHeaders(t, "turn", nil)
	headers.Set(openAISubagentHeader, threadSpawnSubagent)
	headers.Set(threadIDHeader, "child")
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, codexTurnMetadata{
		RequestKind: "turn", ThreadID: "child", ParentThreadID: "root",
		AgentName: "/root/tier-test", SubagentKind: "thread_spawn",
	})))
	request := serverRequest(t, func(fields map[string]any) {
		fields["model"], fields["service_tier"] = "gpt-5.6-sol", "flex"
	})
	provider := &serverFakeProvider{}
	for index, op := range []string{"list", "finish"} {
		body := mustTestJSON(t, map[string]any{
			"id": fmt.Sprintf("tier-response-%d", index), "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "id": fmt.Sprintf("item-%d", index),
				"call_id": fmt.Sprintf("call-%d", index), "name": "journal",
				"arguments": fmt.Sprintf(`{"op":%q}`, op), "status": "completed",
			}},
			"usage": map[string]any{"input_tokens": 50_000},
		})
		provider.results = append(provider.results, serverForwardResult{response: serverHTTPResponse(string(body))})
	}
	executor := requestExecutor{
		provider: provider, output: &bytes.Buffer{}, mekugiCalls: proxy,
		serviceTiers: map[string]string{"gpt-5.6-sol": "fast"},
	}
	if err := executor.execute(t.Context(), t.Context(), request, headers, "child-session"); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 2 {
		t.Fatalf("forwarded=%d", len(provider.forwarded))
	}
	for index, want := range []string{"priority", "priority"} {
		forwarded, err := parseResponsesRequest(provider.forwarded[index])
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonString(forwarded.fields, "service_tier"); got != want {
			t.Fatalf("attempt %d tier=%s want=%s", index, got, want)
		}
	}
	entries := proxy.activity.takeNativeActivity("root")
	if len(entries) != 1 || entries[0].Kind != "start" || entries[0].start == nil || entries[0].start.tier != "fast" {
		t.Fatalf("native start did not retain effective tier: %+v", entries)
	}
}

func TestServiceTierRequestAliases(t *testing.T) {
	for _, tier := range []string{"fast", "priority", "default", "auto", "flex", ""} {
		t.Run(tier, func(t *testing.T) {
			request := serverRequest(t, func(fields map[string]any) {
				if tier != "" {
					fields["service_tier"] = tier
				} else {
					delete(fields, "service_tier")
				}
			})
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestExecutor{provider: provider, output: &bytes.Buffer{}}
			if err := executor.execute(t.Context(), t.Context(), request, http.Header{}, "alias"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := parseResponsesRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			want := tier
			if tier == "fast" {
				want = "priority"
			}
			if got := jsonString(forwarded.fields, "service_tier"); got != want {
				t.Fatalf("wire tier=%q want=%q", got, want)
			}
		})
	}
}
