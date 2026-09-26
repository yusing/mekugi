package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func catalogCacheRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.157.1", nil)
	r.Header = codexAuthHeaders()
	r.Header.Set(sessionIDHeader, "session-one")
	return r
}

func TestModelsHandlerCacheReplaysBodyAndHeaders(t *testing.T) {
	calls := 0
	provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"private", "max-age=60"}, "Etag": {"version-one"}, "X-Private": {"not-forwarded"}}, Body: io.NopCloser(strings.NewReader(`{"models":[{"id":"one"}]}`))}, nil
	})})
	handler := modelsHandler(provider, nil)
	for range 3 {
		w := httptest.NewRecorder()
		handler(w, catalogCacheRequest())
		if w.Code != 200 || w.Body.String() != `{"models":[{"id":"one"}]}` {
			t.Fatalf("response = %d %q", w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("ETag") != "version-one" || strings.Join(w.Header().Values("Cache-Control"), ",") != "private,max-age=60" || w.Header().Get("X-Private") != "" {
			t.Fatalf("headers = %v", w.Header())
		}
		w.Header().Set("ETag", "caller-mutated")
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	modelsHandler(provider, nil)(httptest.NewRecorder(), catalogCacheRequest())
	if calls != 2 {
		t.Fatalf("new handler reused old cache: calls = %d", calls)
	}
}

func TestModelsHandlerCachePartitionsAndReplacesLatestEntry(t *testing.T) {
	for _, field := range []string{"Authorization", "ChatGPT-Account-ID", sessionIDHeader, "query"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return serverHTTPResponse(fmt.Sprintf(`{"generation":%d}`, calls)), nil
			})}), nil)
			for i, want := range []int{1, 1, 2, 2, 3} {
				r := catalogCacheRequest()
				if i == 2 || i == 3 {
					if field == "query" {
						r.URL.RawQuery = "client_version=other"
					} else {
						r.Header.Set(field, "different-value")
						if field == "Authorization" {
							r.Header.Set(field, "Bearer different-token")
						}
					}
				}
				w := httptest.NewRecorder()
				handler(w, r)
				if w.Code != 200 || w.Body.String() != fmt.Sprintf(`{"generation":%d}`, want) || calls != want {
					t.Fatalf("request %d: status %d body %q calls %d, want generation %d", i, w.Code, w.Body.String(), calls, want)
				}
			}
		})
	}
}

func TestModelsHandlerCacheDoesNotStoreFailures(t *testing.T) {
	for _, kind := range []string{"transport", "status", "partial", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return serverHTTPResponse(`{"models":[]}`), nil
				}
				switch kind {
				case "transport":
					return nil, errors.New("temporary failure")
				case "status":
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("retry"))}, nil
				case "partial":
					return &http.Response{StatusCode: 200, Body: io.NopCloser(io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("broken body"))))}, nil
				default:
					return &http.Response{StatusCode: 200, Body: io.NopCloser(io.LimitReader(serverRepeatingReader{}, modelsResponseBufferBytes+1))}, nil
				}
			})}), nil)
			for i := range 3 {
				w := httptest.NewRecorder()
				handler(w, catalogCacheRequest())
				if i == 0 {
					if w.Code < 400 {
						t.Fatalf("failure status = %d", w.Code)
					}
				} else if w.Code != 200 || w.Body.String() != `{"models":[]}` {
					t.Fatalf("retry = %d %q", w.Code, w.Body.String())
				}
			}
			if calls != 2 {
				t.Fatalf("calls = %d, want failed attempt and one success", calls)
			}
		})
	}
}

func TestModelsHandlerWarmCacheStillValidatesAuthentication(t *testing.T) {
	calls := 0
	handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return serverHTTPResponse(`{"models":[]}`), nil })}), nil)
	handler(httptest.NewRecorder(), catalogCacheRequest())
	for _, kind := range []string{"missing", "duplicate"} {
		r := catalogCacheRequest()
		if kind == "missing" {
			r.Header.Del("Authorization")
		} else {
			r.Header.Add("Authorization", "Bearer second-token")
		}
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 502 {
			t.Fatalf("%s authorization status = %d", kind, w.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid authentication forwarded: calls = %d", calls)
	}
}

func TestModelsHandlerCacheCoalescesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return serverHTTPResponse(`{"models":[]}`), nil
	})}), nil)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			handler(w, catalogCacheRequest())
			if w.Code != 200 || w.Body.String() != `{"models":[]}` {
				t.Errorf("response = %d %q", w.Code, w.Body.String())
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestModelsHandlerCacheCanceledWaitDoesNotForward(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return serverHTTPResponse(`{"models":[]}`), nil
	})}), nil)
	go func() { defer close(done); handler(httptest.NewRecorder(), catalogCacheRequest()) }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{})
	go func() { defer close(waiting); handler(httptest.NewRecorder(), catalogCacheRequest().WithContext(ctx)) }()
	cancel()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("canceled waiter remained blocked behind upstream")
	}
	close(release)
	<-done
	if calls.Load() != 1 {
		t.Fatalf("canceled waiter forwarded: calls = %d", calls.Load())
	}
}

type blockedModelsResponseWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
}

func (w *blockedModelsResponseWriter) WriteHeader(code int) {
	close(w.entered)
	<-w.release
	w.ResponseRecorder.WriteHeader(code)
}

func TestModelsHandlerCacheGateReleasedBeforeDownstreamWrite(t *testing.T) {
	for _, mode := range []string{"first success", "cache hit", "upstream error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				n := calls.Add(1)
				if mode == "upstream error" && n == 1 {
					return nil, errors.New("temporary upstream failure")
				}
				return serverHTTPResponse(`{"models":[]}`), nil
			})}), nil)
			if mode == "cache hit" {
				w := httptest.NewRecorder()
				handler(w, catalogCacheRequest())
				if w.Code != 200 {
					t.Fatalf("warmup status = %d", w.Code)
				}
			}
			release := make(chan struct{})
			entered := make(chan struct{})
			firstDone := make(chan struct{})
			writer := &blockedModelsResponseWriter{ResponseRecorder: httptest.NewRecorder(), entered: entered, release: release}
			go func() { defer close(firstDone); handler(writer, catalogCacheRequest()) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				<-firstDone
				t.Fatal("first response did not reach downstream write")
			}
			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); handler(w, catalogCacheRequest()); secondDone <- w }()
			var second *httptest.ResponseRecorder
			select {
			case second = <-secondDone:
			case <-time.After(5 * time.Second):
				close(release)
				<-firstDone
				<-secondDone
				t.Fatal("downstream write held catalog gate")
			}
			close(release)
			<-firstDone
			if second.Code != 200 || second.Body.String() != `{"models":[]}` {
				t.Fatalf("second response = %d %q", second.Code, second.Body.String())
			}
			wantCalls := int32(1)
			if mode == "upstream error" {
				wantCalls = 2
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("upstream calls = %d, want %d", got, wantCalls)
			}
		})
	}
}

func TestModelsHandlerCacheCanceledFetchDoesNotBlockRetry(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	handler := modelsHandler(newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return serverHTTPResponse(`{"models":[]}`), nil
	})}), nil)
	ctx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler(httptest.NewRecorder(), catalogCacheRequest().WithContext(ctx))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		<-firstDone
		t.Fatal("first fetch did not start")
	}
	cancel()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled upstream owner did not exit")
	}
	retryContext, cancelRetry := context.WithCancel(t.Context())
	defer cancelRetry()
	retryDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		handler(w, catalogCacheRequest().WithContext(retryContext))
		retryDone <- w
	}()
	select {
	case w := <-retryDone:
		if w.Code != 200 || w.Body.String() != `{"models":[]}` || calls.Load() != 2 {
			t.Fatalf("retry = %d %q, calls = %d", w.Code, w.Body.String(), calls.Load())
		}
	case <-time.After(5 * time.Second):
		cancelRetry()
		<-retryDone
		t.Fatal("canceled fetch blocked retry")
	}
}
