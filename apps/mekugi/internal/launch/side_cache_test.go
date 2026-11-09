package launch

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func admitCacheTestFork(t *testing.T, requests *sessioncontrol.Requests[appServerRequest], source, workspace, command string) string {
	t.Helper()
	side := &sessioncontrol.Side{SideAnswer: &conversationmodel.SideAnswer{OneOff: command == "oneoff"}, ForkPending: true, LowReasoning: command == "btw"}
	if _, err := requests.StartSide(side, sessioncontrol.SideMain{Thread: source}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	fork, found := requests.Take("cache-fork")
	if !found {
		t.Fatal("missing native fork request")
	}
	thread := source + "-fork"
	result := responses.MustMarshalJSON(map[string]any{"thread": map[string]string{"id": thread, "cwd": workspace}})
	if _, err := requests.ObserveSide(appserver.Message{Result: result}, fork, source); err != nil {
		t.Fatal(err)
	}
	return thread
}

func TestSideCachePreparedPrefix(t *testing.T) {
	for _, command := range []string{"btw", "oneoff"} {
		for _, bodyKey := range []bool{true, false} {
			name := command
			if !bodyKey {
				name += "/omitted-key"
			}
			t.Run(name, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				workspace := t.TempDir()
				requests := sessioncontrol.NewRequests[appServerRequest](appServerTestTransport{input: &appServerTestInput{requestID: "cache-fork"}})
				proxy.bindForkRequests(requests)
				provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}, {response: serverHTTPResponse(`{"status":"completed"}`)}}}
				executor := testRequestExecutor(provider, nil, proxy)
				makeRequest := func(key string) codexresponses.Request {
					return serverRequest(t, func(fields map[string]any) {
						fields["instructions"] = "Keep unrelated base instructions intact."
						if bodyKey {
							fields["prompt_cache_key"] = key
						}
					})
				}
				headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
				headers.Set(codexresponses.ThreadIDHeader, t.Name())
				headers.Set(codexresponses.RoutingSessionHeader, "native-main-session")
				if err := executor.ExecuteHTTP(t.Context(), t.Context(), makeRequest("main-custom-affinity"), headers, io.Discard); err != nil {
					t.Fatal(err)
				}
				thread := admitCacheTestFork(t, requests, t.Name(), workspace, command)
				// No declared execution environment: the confirmed fork cwd must
				// supply workspace identity without changing its native permissions.
				sideHeaders := serverMetadataHeaders(t, "turn", nil)
				sideHeaders.Set(codexresponses.ThreadIDHeader, thread)
				sideHeaders.Set(codexresponses.RoutingSessionHeader, "native-side-session")
				sideRequest := makeRequest("native-side-affinity")
				var items []jsontext.Value
				if err := json.Unmarshal(sideRequest.Fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				items = append(items, responses.MustMarshalJSON(map[string]string{"role": "user", "content": "New side question"}))
				sideRequest.Fields["input"] = responses.MustMarshalJSON(items)
				if err := executor.ExecuteHTTP(t.Context(), t.Context(), sideRequest, sideHeaders, io.Discard); err != nil {
					t.Fatal(err)
				}
				assertSideCachePrefix(t, provider.forwarded[0], provider.forwarded[1])
				if provider.forwardedCacheKey[0] != provider.forwardedCacheKey[1] ||
					provider.forwardedHeaders[1].Get(codexresponses.ThreadIDHeader) != thread ||
					provider.forwardedHeaders[1].Get(codexresponses.RoutingSessionHeader) != "native-side-session" ||
					proxy.review.WorkspaceForThread(thread) != workspace {
					t.Fatal("cache sharing changed affinity, native identity or workspace admission")
				}
			})
		}
	}
}

func assertSideCachePrefix(t *testing.T, mainBody, sideBody []byte) {
	t.Helper()
	main, err := codexresponses.ParseRequest(mainBody)
	if err != nil {
		t.Fatal(err)
	}
	side, err := codexresponses.ParseRequest(sideBody)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instructions", "tools", "model", "reasoning", "prompt_cache_key"} {
		if !bytes.Equal(main.Fields[field], side.Fields[field]) {
			t.Fatalf("fork changed shared %s", field)
		}
	}
	var mainItems, sideItems []jsontext.Value
	if err := json.Unmarshal(main.Fields["input"], &mainItems); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(side.Fields["input"], &sideItems); err != nil {
		t.Fatal(err)
	}
	if len(mainItems) == 0 || len(sideItems) <= len(mainItems) {
		t.Fatal("fork lost inherited input history or its new question")
	}
	for i := range mainItems {
		if !responses.SameJSONValue(mainItems[i], sideItems[i]) {
			t.Fatalf("fork changed inherited input item %d", i)
		}
	}
}

func TestSideCacheWebSocketPrewarmAndTurn(t *testing.T) {
	for _, mode := range []string{"openai-only", "mixed-provider"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			proxy := newManagedMekugiProxy(t)
			requests := sessioncontrol.NewRequests[appServerRequest](appServerTestTransport{input: &appServerTestInput{requestID: "cache-fork"}})
			proxy.bindForkRequests(requests)
			requests.ObserveCacheKey("main", "source-key")
			thread := admitCacheTestFork(t, requests, "main", t.TempDir(), "btw")
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(codexresponses.SessionIDHeader) != "source-key" || codexresponses.ThreadID(r.Header) != thread {
					t.Error("provider upgrade changed cache affinity or native thread identity")
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				for _, id := range []string{"warm", "turn"} {
					body, err := providerSocketRead(ctx, conn)
					if err != nil {
						t.Error(err)
						return
					}
					if responses.StringField(body, "prompt_cache_key") != "source-key" {
						t.Error("provider frame lost source affinity")
					}
					if err := providerSocketWrite(ctx, conn, socketEvent("response.completed", id)); err != nil {
						t.Error(err)
						return
					}
				}
				_, _, _ = conn.Read(ctx)
			}))
			defer provider.Close()
			client := newTestProviderClient(provider.URL, provider.Client())
			if mode == "mixed-provider" {
				client.Grok = &inferencegateway.ThirdPartyClient{}
			}
			endpoint := testWebSocketEndpoint(ctx, time.Second, client, nil, proxy)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			headers := codexAuthHeaders()
			headers.Set(codexresponses.ThreadIDHeader, thread)
			headers.Set(codexresponses.RoutingSessionHeader, "native-side-session")
			conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: headers})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			for _, kind := range []string{"prewarm", "turn"} {
				request := map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{map[string]string{"role": "user", "content": "question"}}, "prompt_cache_key": "native-side-key",
					"client_metadata": map[string]string{codexresponses.TurnMetadataHeader: string(responses.MustMarshalJSON(map[string]string{"request_kind": kind})), codexresponses.ThreadIDHeader: thread}}
				if kind == "prewarm" {
					request["generate"] = false
				}
				socketWrite(t, ctx, conn, request)
				if response := socketRead(t, ctx, conn); responses.StringField(response, "type") != "response.completed" {
					t.Fatalf("side %s failed: %s", kind, responses.MustMarshalJSON(response))
				}
			}
		})
	}
}
