package router

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCodexAuxiliaryRoutesPreserveNativeOperations(t *testing.T) {
	// Keep the public route inventory independent of the registration table.
	paths := []string{
		"alpha/search", "images/generations", "images/edits",
		"alpha/history/v2/list_windows", "alpha/history/v2/list_items", "alpha/history/v2/read_item", "alpha/history/v2/search_contents",
		"alpha/notes/v2/list_files_by_prefix", "alpha/notes/v2/read_file", "alpha/notes/v2/search_contents", "alpha/notes/v2/append_to_file", "alpha/notes/v2/write_file", "alpha/notes/v2/thread_hint",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body := []byte(" {\"opaque\": [1, null], \"input\": \"untouched\"}\n")
			headers := codexAuthHeaders()
			headers.Set("Content-Type", "application/json; charset=utf-8")
			nativeHeaders := []string{threadIDHeader, clientRequestIDHeader, codexWindowIDHeader, codexBetaFeaturesHeader, codexResponsesLiteHeader, openAISubagentHeader, codexTurnMetadataHeader, "x-codex-turn-state", "x-codex-image-turn-id", "x-openai-encrypted-tool-arguments", "x-openai-tool-output-truncation-policy"}
			for _, name := range nativeHeaders {
				headers.Add(name, "opaque-first")
				headers.Add(name, "opaque-second")
			}
			headers.Set("X-Unrelated-Caller-Data", "private")
			calls := 0
			provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodPost || request.URL.String() != testProviderBaseURL+"/"+path+"?cursor=a%2Fb&cursor=c" {
					t.Errorf("upstream request = %s %s", request.Method, request.URL)
				}
				got, err := io.ReadAll(request.Body)
				if err != nil || !bytes.Equal(got, body) {
					t.Errorf("upstream body = %q, error = %v", got, err)
				}
				if request.ContentLength != int64(len(body)) {
					t.Errorf("content length = %d, want %d", request.ContentLength, len(body))
				}
				for _, name := range append(nativeHeaders, "Authorization", chatGPTAccountIDHeader, "Content-Type") {
					if !slices.Equal(request.Header.Values(name), headers.Values(name)) {
						t.Errorf("header %s = %q, want %q", name, request.Header.Values(name), headers.Values(name))
					}
				}
				for _, name := range []string{"Originator", "User-Agent"} {
					if request.Header.Get(name) != codexClientIdentity {
						t.Errorf("%s = %q", name, request.Header.Get(name))
					}
				}
				if request.Header.Get("X-Unrelated-Caller-Data") != "" {
					t.Error("forwarded unrelated caller data")
				}
				response := serverHTTPResponse(" {\"opaque_result\":true}\n")
				response.StatusCode = http.StatusCreated
				response.Header.Set("Content-Type", "application/octet-stream")
				response.Header["Set-Cookie"] = []string{"first=a", "second=b"}
				response.Header.Set("X-Request-ID", "native-id")
				response.Header.Set("Connection", "X-Hop")
				response.Header.Set("X-Hop", "drop")
				return response, nil
			})})
			mux := http.NewServeMux()
			registerCodexAuxiliaryRoutes(mux, t.Context(), time.Minute, provider)
			request := httptest.NewRequest(http.MethodPost, "/v1/"+path+"?cursor=a%2Fb&cursor=c", bytes.NewReader(body))
			request.Header = headers
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if calls != 1 || recorder.Code != http.StatusCreated || recorder.Body.String() != " {\"opaque_result\":true}\n" {
				t.Fatalf("calls=%d response=%d %q", calls, recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Content-Type") != "application/octet-stream" || recorder.Header().Get("X-Request-ID") != "native-id" || !slices.Equal(recorder.Header().Values("Set-Cookie"), []string{"first=a", "second=b"}) {
				t.Errorf("response headers = %v", recorder.Header())
			}
			if recorder.Header().Get("Connection") != "" || recorder.Header().Get("X-Hop") != "" {
				t.Error("forwarded hop-by-hop response headers")
			}
		})
	}
}

func TestCodexAuxiliaryDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusSeeOther} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Location", "/unregistered-write")
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "host-owned redirect")
			}))
			defer upstream.Close()
			mux := http.NewServeMux()
			registerCodexAuxiliaryRoutes(mux, t.Context(), time.Minute, newProviderClient(upstream.URL, upstream.Client()))
			request := httptest.NewRequest("POST", "/v1/alpha/notes/v2/append_to_file", bytes.NewBufferString(`{"text":"once"}`))
			request.Header = codexAuthHeaders()
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if calls.Load() != 1 || recorder.Code != status || recorder.Header().Get("Location") != "/unregistered-write" || recorder.Body.String() != "host-owned redirect" {
				t.Fatalf("calls=%d response=%d headers=%v body=%q", calls.Load(), recorder.Code, recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestCodexAuxiliaryPreservesEncodedStream(t *testing.T) {
	body := codexZstdBody(t, []byte(`{"input":"opaque"}`))
	provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		got, err := io.ReadAll(request.Body)
		if err != nil || !bytes.Equal(got, body) || request.Header.Get("Content-Encoding") != "zstd" || request.ContentLength != -1 || request.GetBody != nil {
			t.Errorf("stream changed: body=%q error=%v encoding=%q length=%d replayable=%t", got, err, request.Header.Get("Content-Encoding"), request.ContentLength, request.GetBody != nil)
		}
		return serverHTTPResponse("{}"), nil
	})})
	mux := http.NewServeMux()
	registerCodexAuxiliaryRoutes(mux, t.Context(), time.Minute, provider)
	request := httptest.NewRequest("POST", "/v1/images/generations", io.NopCloser(bytes.NewReader(body)))
	request.Header = codexAuthHeaders()
	request.Header.Set("Content-Encoding", "zstd")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestCodexAuxiliaryRejectsUnsupportedRequests(t *testing.T) {
	for _, test := range []struct {
		name, method, path        string
		authenticated, thirdParty bool
		status                    int
	}{
		{"unauthenticated", "POST", "/v1/alpha/search", false, false, http.StatusBadRequest},
		{"third party", "POST", "/v1/alpha/search", true, true, http.StatusNotFound},
		{"unknown endpoint", "POST", "/v1/alpha/unknown", true, false, http.StatusNotFound},
		{"wrong method", "GET", "/v1/alpha/search", true, false, http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return serverHTTPResponse("{}"), nil })})
			provider.thirdPartyOnly = test.thirdParty
			mux := http.NewServeMux()
			registerCodexAuxiliaryRoutes(mux, t.Context(), time.Minute, provider)
			request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString("{}"))
			if test.authenticated {
				request.Header = codexAuthHeaders()
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.status || calls != 0 {
				t.Fatalf("status=%d calls=%d, want %d and no forward", recorder.Code, calls, test.status)
			}
		})
	}
}

func TestCodexAuxiliaryFailuresNeverRetryWrites(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream rejection", true: "transport failure"}[transportFailure], func(t *testing.T) {
			calls := 0
			provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if transportFailure {
					return nil, errors.New("connection failed")
				}
				response := serverHTTPResponse(`{"error":"quota"}`)
				response.StatusCode = http.StatusTooManyRequests
				response.Header.Set("Retry-After", "17")
				return response, nil
			})})
			mux := http.NewServeMux()
			registerCodexAuxiliaryRoutes(mux, t.Context(), time.Minute, provider)
			request := httptest.NewRequest("POST", "/v1/alpha/notes/v2/append_to_file", bytes.NewBufferString(`{"text":"append once"}`))
			request.Header = codexAuthHeaders()
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			want := http.StatusTooManyRequests
			if transportFailure {
				want = http.StatusBadGateway
			}
			if calls != 1 || recorder.Code != want {
				t.Fatalf("calls=%d status=%d, want one call and %d", calls, recorder.Code, want)
			}
			if !transportFailure && (recorder.Body.String() != `{"error":"quota"}` || recorder.Header().Get("Retry-After") != "17") {
				t.Fatalf("upstream error changed: %v %q", recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestCodexAuxiliaryResponseStartTimeoutCancelsUpstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		provider := newProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
			calls++
			<-request.Context().Done()
			return nil, request.Context().Err()
		})})
		mux := http.NewServeMux()
		registerCodexAuxiliaryRoutes(mux, t.Context(), time.Second, provider)
		request := httptest.NewRequest("POST", "/v1/alpha/search", bytes.NewBufferString("{}"))
		request.Header = codexAuthHeaders()
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if calls != 1 || recorder.Code != http.StatusBadGateway {
			t.Fatalf("calls=%d status=%d", calls, recorder.Code)
		}
	})
}
