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

	"github.com/coder/websocket"
)

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
				endpoint := responsesWebSocketHandler(ctx, 10*time.Second, client, nil, nil, nil, nil)
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
				responsesHandler(ctx, 10*time.Second, client, nil, nil, nil, nil)(recorder, req)
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
	for _, mentorEnabled := range []bool{false, true} {
		t.Run(fmt.Sprint(mentorEnabled), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			request := serverRequest(t, func(fields map[string]any) {
				fields["model"], fields["service_tier"] = "gpt-5.6-luna", "flex"
			})
			headers := serverMetadataHeaders(t, "turn", nil)
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			executor := requestExecutor{provider: provider, output: &bytes.Buffer{}, mekugiCalls: proxy,
				serviceTiers: map[string]string{"gpt-6-astra": "fast"}}
			if mentorEnabled {
				executor.mentor = newMentorHandoff(true, true)
			}
			if err := executor.execute(t.Context(), t.Context(), request, headers, "tier"); err != nil {
				t.Fatal(err)
			}
			forwarded, err := parseResponsesRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			want := "flex"
			if mentorEnabled {
				want = "priority"
			}
			if got := jsonString(forwarded.fields, "service_tier"); got != want {
				t.Fatalf("tier=%s want=%s", got, want)
			}
			if got := subagentStartCommentary(&forwarded, ""); !strings.Contains(got, "Service tier: `"+strings.ReplaceAll(want, "priority", "fast")+"`") {
				t.Fatalf("commentary=%s", got)
			}
		})
	}
}

func TestSubagentStartServiceTier(t *testing.T) {
	for _, tc := range []struct{ tier, want string }{
		{"priority", "`fast`"}, {"fast", "`fast`"}, {"default", "`default`"}, {"", "not specified"},
	} {
		request := serverRequest(t, func(fields map[string]any) {
			fields["model"] = "gpt-6-astra"
			if tc.tier != "" {
				fields["service_tier"] = tc.tier
			}
		})
		if got := subagentStartCommentary(&request, ""); !strings.Contains(got, "Service tier: "+tc.want) {
			t.Fatalf("tier=%s commentary=%s", tc.tier, got)
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

func TestServiceTierJournalHandoffAndRootNotice(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
	defer root.Close()
	headers := mentorTestHeaders(t, "child")
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
			"usage": map[string]any{"input_tokens": mentorInputTokenLimit},
		})
		provider.results = append(provider.results, serverForwardResult{response: serverHTTPResponse(string(body))})
	}
	executor := requestExecutor{
		provider: provider, output: &bytes.Buffer{}, mekugiCalls: proxy,
		mentor: newMentorHandoff(true, true), serviceTiers: map[string]string{"gpt-6-astra": "fast"},
	}
	if err := executor.execute(t.Context(), t.Context(), request, headers, "child-session"); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 2 {
		t.Fatalf("forwarded=%d", len(provider.forwarded))
	}
	for index, want := range []string{"priority", "flex"} {
		forwarded, err := parseResponsesRequest(provider.forwarded[index])
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonString(forwarded.fields, "service_tier"); got != want {
			t.Fatalf("attempt %d tier=%s want=%s", index, got, want)
		}
	}
	notices := proxy.activity.drain("root", root.activityStarted, maxCommentaryPublicationBytes)
	for _, notice := range notices {
		text := commentaryText(t, notice)
		if strings.Contains(text, "Started.") && strings.Contains(text, "Service tier: `fast`") {
			return
		}
	}
	t.Fatalf("root did not receive effective start tier: %s", mustTestJSON(t, notices))
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
