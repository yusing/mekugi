package inferencegateway

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

func nativeTestClient(endpoint string, client *http.Client) *Client {
	p := newTestClient("http://openai-must-not-be-used.invalid", client)
	p.Native = &NativeProvider{ID: "fixture", BaseURL: endpoint, HTTPClient: client, StreamIdleTimeout: time.Second}
	return p
}

func TestNativeProviderExplicitIdleTimeoutAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		started := make(chan struct{})
		p := nativeTestClient("http://fixture.invalid", &http.Client{Transport: nativeRoundTripper(func(r *http.Request) (*http.Response, error) {
			close(started)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
		})})
		p.Native.StreamIdleTimeout = 15 * time.Minute
		response, err := p.ForwardExecution(t.Context(), t.Context(), []byte(`{"model":"unknown"}`), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		done := make(chan error, 1)
		go func() { _, err := io.ReadAll(response.Body); done <- err }()
		<-started
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		select {
		case err := <-done:
			t.Fatalf("explicit provider idle timeout truncated by gateway default: %v", err)
		default:
		}
		time.Sleep(10 * time.Minute)
		if err := <-done; err == nil {
			t.Fatal("explicit provider idle timeout did not close stream")
		}
	})
}

type nativeRoundTripper func(*http.Request) (*http.Response, error)

func (f nativeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeProviderPinnedHTTPHeadersQueriesAndModels(t *testing.T) {
	var calls atomic.Int32
	const body = `{"model":"opencode-go:literal","input":[],"stream":false,"reasoning":{"effort":"ultra"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.EscapedPath() != "/prefix%2Ftenant/v1/responses" || r.URL.RawQuery != "base=a%2Bb&native=x%252Fy" {
			t.Errorf("endpoint fidelity: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Provider-Header") != "literal" || r.Header.Get("X-Mekugi-Local") != "" || r.Header.Get("X-Hop") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Content-Encoding") != "" {
			t.Error("provider header fidelity/isolation failed")
		}
		encoded, _ := io.ReadAll(r.Body)
		if string(encoded) != body {
			t.Errorf("native wire changed: %s", encoded)
		}
		w.Header().Set("X-Provider-Response", "preserved")
		w.Header().Set("Connection", "X-Provider-Hop")
		w.Header().Set("X-Provider-Hop", "remove")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"native","status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	p := nativeTestClient(upstream.URL+"/prefix%2Ftenant/v1?base=a%2Bb", upstream.Client())
	headers := http.Header{"Authorization": {"Bearer fixture"}, "X-Provider-Header": {"literal"}, "X-Mekugi-Local": {"private"}, "Connection": {"X-Hop"}, "X-Hop": {"remove"}, "Proxy-Authorization": {"remove"}, "Content-Encoding": {"gzip"}}
	ctx := WithProviderQuery(t.Context(), "native=x%252Fy")
	response, err := p.ForwardExecution(ctx, ctx, []byte(body), headers, "")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !response.UnknownPrice || response.Price != nil || response.Header.Get("X-Provider-Response") != "preserved" || response.Header.Get("X-Provider-Hop") != "" || response.Provider != "fixture" || calls.Load() != 1 {
		t.Fatalf("native exchange facts: %+v calls=%d", response, calls.Load())
	}
	if p.ApplyOpenAIPolicy("gpt-6-sol") || p.NativeContinuations("gpt-6-sol") {
		t.Fatal("HTTP native route acquired built-in policy or socket ancestry")
	}
}

func TestNativeProviderErrorsRedirectsCancellation(t *testing.T) {
	for _, status := range []int{302, 401, 403, 429, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var reached atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
			defer destination.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.Header().Set("X-Provider-Error", "preserved")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"type":"fixture_rejection","message":"exact upstream error"}}`)
			}))
			defer upstream.Close()
			p := nativeTestClient(upstream.URL, upstream.Client())
			_, err := p.ForwardExecution(t.Context(), t.Context(), []byte(`{"model":"grok:literal"}`), http.Header{"Authorization": {"Bearer fixture"}}, "")
			rejection, ok := errors.AsType[*ProviderHTTPError](err)
			if !ok || rejection.Status != status || !strings.Contains(err.Error(), "fixture returned HTTP") || !strings.Contains(err.Error(), "exact upstream error") || reached.Load() != 0 {
				t.Fatalf("failure status/identity/redirect: %v reached=%d", err, reached.Load())
			}
			writer := &ResponseWriter{ResponseWriter: httptest.NewRecorder()}
			writeRequestError(writer, err)
			if writer.Header().Get("X-Provider-Error") != "preserved" || writer.StatusCode() != status {
				t.Fatal("downstream error lost provider headers or status")
			}
		})
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer upstream.Close()
	p := nativeTestClient(upstream.URL, upstream.Client())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := p.ForwardExecution(ctx, ctx, []byte(`{}`), nil, ""); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("provider work was not canceled")
	}
}

func TestNativeProviderRemoteCatalogAndAuxiliaryAdmission(t *testing.T) {
	var catalogs, images, searches, forbidden atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom-Auth") != "fixture" {
			t.Error("catalog/auxiliary provider header lost")
		}
		switch r.URL.Path {
		case "/catalog/source":
			catalogs.Add(1)
			if r.URL.RawQuery != "catalog=a%2Bb&client_version=fixture" {
				t.Errorf("catalog query: %s", r.URL)
			}
			w.Header().Set("ETag", "original")
			io.WriteString(w, `{"models":[{"slug":"arbitrary","tool_mode":"code_mode","future":42}]}`)
		case "/prefix/images/generations":
			images.Add(1)
			w.Header().Set("X-Custom-Reply", "preserved")
			w.WriteHeader(201)
			io.Copy(w, r.Body)
		case "/prefix/alpha/search":
			searches.Add(1)
			w.WriteHeader(http.StatusNotImplemented)
			io.WriteString(w, "provider does not support search")
		default:
			forbidden.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer upstream.Close()
	p := nativeTestClient(upstream.URL+"/prefix", upstream.Client())
	p.Native.CatalogURL = upstream.URL + "/catalog/source?catalog=a%2Bb"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", ModelsHandler(p, nil, nil))
	RegisterCodexAuxiliaryRoutes(mux, t.Context(), time.Second, p)
	for _, path := range []string{"/v1/models?client_version=fixture", "/v1/images/generations", "/v1/alpha/search", "/v1/alpha/notes/v2/read_file"} {
		method := http.MethodPost
		if strings.Contains(path, "models") {
			method = http.MethodGet
		}
		r := httptest.NewRequest(method, path, strings.NewReader("encoded auxiliary"))
		r.Header.Set("X-Custom-Auth", "fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if method == http.MethodGet {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"tool_mode":"code_mode_only"`) || !strings.Contains(w.Body.String(), `"future":42`) || w.Header().Get("ETag") != "original" {
				t.Fatalf("catalog projection: %d %s", w.Code, w.Body)
			}
		} else if strings.Contains(path, "images") {
			if w.Code != 201 || w.Body.String() != "encoded auxiliary" || w.Header().Get("X-Custom-Reply") != "preserved" {
				t.Fatalf("auxiliary: %d %s", w.Code, w.Body)
			}
		} else if strings.Contains(path, "alpha/search") {
			if w.Code != http.StatusNotImplemented || w.Body.String() != "provider does not support search" {
				t.Fatalf("provider search rejection: %d %s", w.Code, w.Body)
			}
		} else if w.Code != 404 {
			t.Fatalf("unavailable operation: %d", w.Code)
		}
	}
	if catalogs.Load() != 1 || images.Load() != 1 || searches.Load() != 1 || forbidden.Load() != 0 {
		t.Fatalf("routing: catalogs=%d images=%d searches=%d forbidden=%d", catalogs.Load(), images.Load(), searches.Load(), forbidden.Load())
	}
}

func TestNativeProviderWebSocketHandshakeNoOpenAIAccount(t *testing.T) {
	var upgrades, messages atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrades.Add(1)
		if r.URL.RequestURI() != "/prefix/responses?fixed=a%2Bb&native=x%252Fy" || r.Header.Get("X-Provider") != "fixture" || r.Header.Get("Chatgpt-Account-Id") != "" {
			t.Error("native handshake fidelity failed")
		}
		w.Header().Set("X-Reasoning-Included", "true")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			messages.Add(1)
		}
	}))
	defer upstream.Close()
	p := nativeTestClient(upstream.URL+"/prefix?fixed=a%2Bb", upstream.Client())
	p.Native.WebSockets = true
	endpoint := NewWebSocketEndpoint(t.Context(), time.Second, p, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, response, err := websocket.Dial(t.Context(), server.URL+"?native=x%252Fy", &websocket.DialOptions{HTTPHeader: http.Header{"X-Provider": {"fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	conn.CloseNow()
	if response.Header.Get("X-Reasoning-Included") != "true" || upgrades.Load() != 1 || messages.Load() != 0 {
		t.Fatalf("handshake generated inference or lost signal: %d %d", upgrades.Load(), messages.Load())
	}
	if !p.NativeContinuations("opencode-go:literal") {
		t.Fatal("native socket lost ancestry")
	}
}

func TestNativeProviderWebSocketRequestKeepsCustomHeaders(t *testing.T) {
	p := nativeTestClient("http://unused.invalid", http.DefaultClient)
	p.Native.WebSockets = true
	payload, headers, err := p.webSocketRequest([]byte(`{"model":"grok:literal","input":[],"stream":true}`), http.Header{"X-Provider": {"fixture"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["model"] != "grok:literal" || fields["type"] != "response.create" || headers.Get("X-Provider") != "fixture" {
		t.Fatalf("native socket projection: %s", payload)
	}
}
