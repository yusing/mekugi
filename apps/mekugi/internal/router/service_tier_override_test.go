package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/libraries/diffrender"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

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
	config, err := launchoptions.LoadSettings(false)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, directory)
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	u.serviceTiers = inferencegateway.NewTiers(config.ServiceTiers)
	u.sessionPresentation().Status.Text = "Ready"
	u.view.Painter.Theme = diffrender.DarkTheme
	for _, tc := range []struct{ model, requested, want string }{
		{"gpt-6.1-sol", "default", "priority"},
		{"gpt-6-astra", "priority", "flex"},
		{"unconfigured", "default", "default"},
		{"unconfigured", "fast", "priority"},
	} {
		t.Run(tc.model+"-"+tc.requested, func(t *testing.T) {
			appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "main", "threadSettings": map[string]any{"model": tc.model, "effort": "high", "serviceTier": tc.requested}})
			rows, _ := u.testMainFrame(100, 10, 0)
			if frame := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(frame, tc.model+" (high) · "+tc.want) {
				t.Fatalf("composer does not reflect routed tier: %s", frame)
			}
			if tc.model == "gpt-6.1-sol" {
				assertNativeUISnapshot(t, "native-main-configured-tier", rows)
			}
			// Passthrough routes tiers too, but has no observation proxy.
			setTestAppServerIntegration(u, nil)
			passthrough, _ := u.testMainFrame(100, 10, 0)
			if strings.Join(rows, "\n") != strings.Join(passthrough, "\n") {
				t.Fatal("passthrough composer lost configured tier")
			}
			appServerTestKeys(t, u, "/status\r")
			if body := statusReportText(u.statusPanel); !strings.Contains(body, "Service tier: "+tc.want) {
				t.Fatalf("status does not reflect routed tier: %s", body)
			}
			u.statusPanelKey("\x1b")
			setTestAppServerIntegration(u, proxy)
			if u.serviceTier != tc.requested || u.model != tc.model {
				t.Fatal("presentation replaced host settings")
			}
			request := serverRequest(t, func(fields map[string]any) { fields["model"], fields["service_tier"] = tc.model, tc.requested })
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestexecution.Executor{Provider: provider, Output: &bytes.Buffer{}, ServiceTiers: inferencegateway.NewTiers(config.ServiceTiers)}
			if err := executor.Execute(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := responses.StringField(forwarded.Fields, "service_tier"); got != tc.want {
				t.Fatalf("provider tier=%s, display=%s", got, tc.want)
			}
		})
	}
}

func newServiceTierPickerTestUI(t *testing.T, model, tier string, overrides map[string]string) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, input := newAppServerTestUI()
	u.ctx = t.Context()
	u.model, u.serviceTier = model, tier
	u.serviceTiers = inferencegateway.NewTiers(overrides)
	u.view.Painter.Theme = diffrender.DarkTheme
	if err := json.Unmarshal([]byte(`[{"model":"gpt-6.1-sol","serviceTiers":[{"id":"priority"},{"id":"flex"}]},{"model":"gpt-5.6-terra","serviceTiers":[{"id":"priority"},{"id":"flex"}]},{"model":"unconfigured","serviceTiers":[{"id":"priority"},{"id":"flex"}]}]`), &u.models); err != nil {
		t.Fatal(err)
	}
	return u, input
}

func TestUISnapshotNativeTierPicker(t *testing.T) {
	for _, tc := range []struct {
		name, model, hostTier string
		overrides             map[string]string
		selected              int
	}{
		{"configured", "gpt-6.1-sol", "", map[string]string{"gpt-6.1-sol": "fast"}, 1},
		{"unconfigured", "unconfigured", "priority", nil, 1},
		{"legacy-terra", "gpt-5.6-terra", "flex", map[string]string{"gpt-6-sol": "fast"}, 1},
		{"configured-flex", "gpt-6.1-sol", "default", map[string]string{"gpt-6.1-sol": "flex"}, 2},
	} {
		for _, width := range []int{36, 100} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, width), func(t *testing.T) {
				u, input := newServiceTierPickerTestUI(t, tc.model, tc.hostTier, tc.overrides)
				appServerTestKeys(t, u, "/tier\r")
				if !u.picker.Open || u.settingsChoices != "/tier" || u.picker.Selected != tc.selected {
					t.Fatalf("picker did not select effective tier: %+v", u.picker)
				}
				if input.Len() != 0 || u.serviceTier != tc.hostTier || u.model != tc.model {
					t.Fatal("opening picker changed host settings")
				}
				assertNativeUISnapshot(t, fmt.Sprintf("native-tier-%s-%d", tc.name, width), u.renderPicker(width, 7))
			})
		}
	}
}

func TestUISnapshotNativeTierPickerModelSwitch(t *testing.T) {
	u, _ := newServiceTierPickerTestUI(t, "gpt-6.1-sol", "default", map[string]string{"gpt-6.1-sol": "fast"})
	appServerTestKeys(t, u, "/tier\r")
	appServerTestKeys(t, u, "\x1b")
	appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "main", "threadSettings": map[string]any{"model": "unconfigured", "effort": "high", "serviceTier": "priority"}})
	appServerTestKeys(t, u, "/tier\r")
	if u.picker.Problem != "" || u.picker.Selected != 1 || u.serviceTier != "priority" || u.model != "unconfigured" {
		t.Fatalf("model switch retained override or lost host settings: picker=%+v model=%s tier=%s", u.picker, u.model, u.serviceTier)
	}
	assertNativeUISnapshot(t, "native-tier-model-switch-100", u.renderPicker(100, 7))
}

func TestUISnapshotNativeTierPickerCatalogStates(t *testing.T) {
	for _, state := range []string{"loading", "failed"} {
		for _, width := range []int{36, 100} {
			t.Run(fmt.Sprintf("%s/%d", state, width), func(t *testing.T) {
				u, _ := newServiceTierPickerTestUI(t, "gpt-6.1-sol", "default", map[string]string{"gpt-6.1-sol": "fast"})
				u.modelsLoading = true
				appServerTestRequest(t, u, "99", "model/list")
				appServerTestKeys(t, u, "/tier\r")
				if state == "failed" {
					appServerTestMessage(t, u, `{"id":99,"error":{"code":-1,"message":"catalog unavailable"}}`)
					if u.modelsLoading || u.picker.Loading || u.picker.Problem == "" {
						t.Fatal("catalog failure did not end loading and preserve its error")
					}
				} else if !u.picker.Loading {
					t.Fatal("picker did not expose catalog loading state")
				}
				assertNativeUISnapshot(t, fmt.Sprintf("native-tier-%s-%d", state, width), u.renderPicker(width, 7))
			})
		}
	}
}

func TestServiceTierPickerSelectionUpdatesEffectiveTier(t *testing.T) {
	for _, tc := range []struct {
		name, initial, keys string
		want                any
		applied             string
	}{
		{"flex", "", "\x1b[F\r", "flex", "flex"},
		{"default", "flex", "\x1b[H\r", nil, ""},
		{"default-noop", "", "\x1b[H\r", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, input := newServiceTierPickerTestUI(t, "gpt-6.1-sol", tc.initial, map[string]string{"gpt-6.1-sol": "fast"})
			appServerTestKeys(t, u, "/tier\r"+tc.keys)
			if tc.initial != tc.applied {
				assertAppServerSettingsRequest(t, input, "thread/settings/update", map[string]any{"threadId": "main", "serviceTier": tc.want})
				if u.picker.Open || !u.Settings.Pending() || u.serviceTier != tc.initial {
					t.Fatal("selection replaced host settings before confirmation")
				}
				appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "main", "threadSettings": map[string]any{"model": "gpt-6.1-sol", "effort": "high", "serviceTier": tc.want}})
			} else if input.Len() != 0 || u.Settings.Pending() {
				t.Fatal("unchanged host value waited for a notification")
			}
			want := tc.applied
			if want == "" {
				want = "default"
			}
			if u.displayServiceTier() != want || u.serviceTiers.Effective(u.model, "default", "unrelated") != "priority" {
				t.Fatal("selection did not replace effective tier or changed configuration")
			}
			request := serverRequest(t, func(fields map[string]any) { fields["model"], fields["service_tier"] = u.model, u.serviceTier })
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestexecution.Executor{Provider: provider, Output: &bytes.Buffer{}, ServiceTiers: u.serviceTiers}
			headers := serverMetadataHeaders(t, "turn", nil)
			headers.Set(codexresponses.ThreadIDHeader, u.Thread)
			if err := executor.Execute(t.Context(), t.Context(), request, headers, "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := responses.StringField(forwarded.Fields, "service_tier"); got != want {
				t.Fatalf("provider tier=%s want=%s", got, want)
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
			client := newTestProviderClient(upstream.URL, upstream.Client())
			executor := requestexecution.Executor{Provider: client, Client: client, ServiceTiers: inferencegateway.NewTiers(map[string]string{"gpt-6-astra": "priority"})}
			request := serverRequest(t, func(fields map[string]any) {
				fields["model"], fields["service_tier"], fields["stream"] = "gpt-6-astra", "default", transport != "http"
			})
			if transport == "websocket" {
				endpoint := inferencegateway.NewWebSocketEndpoint(ctx, 10*time.Second, client, executor.ExecuteWebSocket)
				defer endpoint.Close()
				server := httptest.NewServer(endpoint)
				defer server.Close()
				conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				request.Fields["type"] = responses.MustMarshalJSON("response.create")
				socketWrite(t, ctx, conn, request.Fields)
				event := socketRead(t, ctx, conn)
				if responses.StringField(event, "type") != "response.completed" {
					t.Fatalf("event=%s", event)
				}
			} else {
				req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(mustTestJSON(t, request.Fields)))
				req.Header = codexAuthHeaders()
				recorder := httptest.NewRecorder()
				responsesHandler(ctx, 10*time.Second, executor)(recorder, req)
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
			executor := testRequestExecutor(provider, nil, proxy)
			executor.Output, executor.ServiceTiers = &bytes.Buffer{}, inferencegateway.NewTiers(map[string]string{"gpt-6-sol": "fast"})
			if err := executor.Execute(t.Context(), t.Context(), request, headers, "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			wantTier, wantModel := "flex", model
			if model == "gpt-5.6-terra" {
				wantTier, wantModel = "priority", "gpt-6-sol"
			}
			if got := responses.StringField(forwarded.Fields, "service_tier"); got != wantTier {
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

func TestServiceTierChildRequestAndRootNotice(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
	proxy.activity.Attach("root")
	defer root.Close()
	headers := serverMetadataHeaders(t, "turn", nil)
	headers.Set(codexresponses.SubagentHeader, threadSpawnSubagent)
	headers.Set(codexresponses.ThreadIDHeader, "child")
	headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, codexresponses.TurnMetadata{
		RequestKind: "turn", ThreadID: "child", ParentThreadID: "root",
		AgentName: "/root/tier-test", SubagentKind: "thread_spawn",
	})))
	request := serverRequest(t, func(fields map[string]any) {
		fields["model"], fields["service_tier"] = "gpt-5.6-sol", "flex"
	})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"tier-response","status":"completed","output":[],"usage":{"input_tokens":50000}}`)}}}
	executor := testRequestExecutor(provider, nil, proxy)
	executor.Output, executor.ServiceTiers = &bytes.Buffer{}, inferencegateway.NewTiers(map[string]string{"gpt-5.6-sol": "fast"})
	if err := executor.Execute(t.Context(), t.Context(), request, headers, "child-session"); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("forwarded=%d", len(provider.forwarded))
	}
	forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := responses.StringField(forwarded.Fields, "service_tier"); got != "priority" {
		t.Fatalf("provider tier=%s want=priority", got)
	}
	entries := takeNativeActivity(proxy.activity, "root")
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
			executor := requestexecution.Executor{Provider: provider, Output: &bytes.Buffer{}}
			if err := executor.Execute(t.Context(), t.Context(), request, http.Header{}, "alias"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			want := tier
			if tier == "fast" {
				want = "priority"
			}
			if got := responses.StringField(forwarded.Fields, "service_tier"); got != want {
				t.Fatalf("wire tier=%q want=%q", got, want)
			}
		})
	}
}
