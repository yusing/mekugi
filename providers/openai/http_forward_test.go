package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

type httpForwardTransport func(*http.Request) (*http.Response, error)

func (f httpForwardTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResponsesHTTPForwardsCapacityRejectionOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const body = " {\"error\":{\"code\":\"capacity_exceeded\",\"message\":\"Selected model is at capacity\"}}\n"
		calls := 0
		client := New("https://provider.invalid", &http.Client{Transport: httpForwardTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodPost || r.URL.Path != "/responses" || r.Header.Get("Upgrade") != "" {
				t.Errorf("unexpected HTTP attempt: %s %s, headers=%v", r.Method, r.URL, r.Header)
			}
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {"65"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		response, err := client.Responses(t.Context(), t.Context(), []byte(`{"model":"gpt-test"}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		got, err := io.ReadAll(response.Body)
		if err != nil || string(got) != body || response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "65" || calls != 1 {
			t.Fatalf("capacity response: status=%d retry=%q body=%q calls=%d err=%v", response.StatusCode, response.Header.Get("Retry-After"), got, calls, err)
		}
	})
}

func TestResponsesHTTPStartupCancellation(t *testing.T) {
	start, cancel := context.WithCancel(t.Context())
	cancel()
	client := New("https://provider.invalid", &http.Client{Transport: httpForwardTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	response, err := client.Responses(start, t.Context(), []byte(`{}`), nil)
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup: response=%v err=%v", response, err)
	}
}

func TestResponsesHTTPBodyOwnsLifetimeAfterStartup(t *testing.T) {
	for _, end := range []string{"close", "response cancellation"} {
		t.Run(end, func(t *testing.T) {
			start, cancelStart := context.WithCancel(t.Context())
			defer cancelStart()
			lifetime, cancelLifetime := context.WithCancel(t.Context())
			defer cancelLifetime()
			var requestContext context.Context
			client := New("https://provider.invalid", &http.Client{Transport: httpForwardTransport(func(r *http.Request) (*http.Response, error) {
				requestContext = r.Context()
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("stream"))}, nil
			})})
			response, err := client.Responses(start, lifetime, []byte(`{}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			cancelStart()
			got, err := io.ReadAll(response.Body)
			if err != nil || string(got) != "stream" || requestContext.Err() != nil {
				t.Fatalf("startup cancellation ended body lifetime: body=%q read=%v context=%v", got, err, requestContext.Err())
			}
			if end == "close" {
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancelLifetime()
			}
			if requestContext.Err() != context.Canceled {
				t.Fatalf("body lifetime still active after %s: %v", end, requestContext.Err())
			}
		})
	}
}

func TestResponsesHTTPPreservesRedirectWithoutReplay(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := New("https://provider.invalid", &http.Client{Transport: httpForwardTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("replayed"))}, nil
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"/redirected"}, "Retry-After": {"65"}}, Body: io.NopCloser(strings.NewReader("redirect response"))}, nil
			})})
			response, err := client.Responses(t.Context(), t.Context(), []byte(`{"model":"gpt-test"}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || calls != 1 || response.StatusCode != status || response.Header.Get("Retry-After") != "65" || response.Header.Get("Location") != "/redirected" || string(body) != "redirect response" {
				t.Fatalf("redirect changed: calls=%d status=%d headers=%v body=%q err=%v", calls, response.StatusCode, response.Header, body, err)
			}
		})
	}
}
