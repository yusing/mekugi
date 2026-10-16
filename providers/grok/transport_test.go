package grok

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

type inferenceBody struct {
	closed bool
}

func (*inferenceBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *inferenceBody) Close() error           { b.closed = true; return nil }

func TestForwardOAuthRefreshBoundary(t *testing.T) {
	for _, outcome := range []string{"success", "unauthorized", "send-error"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "auth.json")
			grokWriteTestAuth(t, path, time.Now().Add(time.Hour))
			first, final := &inferenceBody{}, &inferenceBody{}
			cause := errors.New("inference connection lost")
			var sends, discoveries, refreshes int
			body := []byte(" {\"opaque\": true}\n")
			client := &http.Client{Transport: grokTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth.x.ai" {
					if !first.closed {
						t.Fatal("401 body remained open during refresh")
					}
					if r.Method == http.MethodGet {
						discoveries++
						return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
					}
					refreshes++
					return serverHTTPResponse(`{"access_token":"fresh-token","expires_in":3600}`), nil
				}
				sends++
				if sends > 2 {
					t.Fatal("OAuth inference retried more than once")
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != string(body) {
					t.Fatalf("send %d body=%q err=%v", sends, got, err)
				}
				token := "old-token"
				if sends > 1 {
					token = "fresh-token"
				}
				want := http.Header{
					"Authorization": {"Bearer " + token}, "User-Agent": {"mekugi"},
					"X-Xai-Token-Auth": {"xai-grok-cli"}, "X-Grok-Client-Identifier": {"mekugi"},
					"X-Grok-Client-Version": {"1.0.13"}, "X-Grok-Client-Mode": {"headless"},
					"X-Grok-Model-Override": {"grok-4.7-build-fast"},
					"Content-Type":          {"application/json"}, "Accept": {"text/event-stream"},
				}
				if r.URL.String() != grokProxyEndpoint || r.Method != http.MethodPost || !reflect.DeepEqual(r.Header, want) {
					t.Fatalf("unexpected inference request: %s %s headers=%v", r.Method, r.URL, r.Header)
				}
				if sends == 1 {
					return &http.Response{StatusCode: http.StatusUnauthorized, Body: first}, nil
				}
				if outcome == "send-error" {
					return nil, cause
				}
				status := http.StatusOK
				if outcome == "unauthorized" {
					status = http.StatusUnauthorized
				}
				return &http.Response{StatusCode: status, Body: final}, nil
			})}
			response, err := Forward(t.Context(), t.Context(), client, NewAuth(path, "", client), "grok-4.7-build-fast", body)
			if outcome == "send-error" {
				_, sendFailure := errors.AsType[*httptransport.SendError](err)
				if response != nil || !errors.Is(err, cause) || !sendFailure {
					t.Fatalf("lost retry send cause: response=%v err=%v", response, err)
				}
			} else {
				wantStatus := http.StatusOK
				if outcome == "unauthorized" {
					wantStatus = http.StatusUnauthorized
				}
				if err != nil || response == nil || response.StatusCode != wantStatus || response.Body != final || final.closed {
					t.Fatalf("final response ownership lost: response=%v err=%v closed=%v", response, err, final.closed)
				}
				response.Body.Close()
			}
			if sends != 2 || discoveries != 1 || refreshes != 1 {
				t.Fatalf("sends=%d discovery=%d refresh=%d", sends, discoveries, refreshes)
			}
		})
	}
}

func TestForwardAPIKeyBoundary(t *testing.T) {
	for _, outcome := range []string{"redirect", "unauthorized", "send-error", "build-fast"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			calls, redirects := 0, 0
			cause := errors.New("connection lost")
			client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
				redirects++
				return nil
			}}
			client.Transport = grokTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "probe.invalid" {
					response := serverHTTPResponse("")
					if r.URL.Path == "/start" {
						response.StatusCode = http.StatusTemporaryRedirect
						response.Header.Set("Location", "https://probe.invalid/end")
					}
					return response, nil
				}
				calls++
				want := http.Header{"Authorization": {"Bearer api-key"}, "User-Agent": {"mekugi"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"}}
				if r.URL.String() != grokAPIEndpoint || !reflect.DeepEqual(r.Header, want) {
					t.Fatalf("unexpected API request: %s headers=%v", r.URL, r.Header)
				}
				if outcome == "send-error" {
					return nil, cause
				}
				response := serverHTTPResponse("")
				response.StatusCode = http.StatusUnauthorized
				if outcome == "redirect" {
					response.StatusCode = http.StatusTemporaryRedirect
					response.Header.Set("Location", "https://attacker.invalid/inference")
				}
				return response, nil
			})
			model := "grok-model"
			if outcome == "build-fast" {
				model = "grok-4.7-build-fast"
			}
			response, err := Forward(t.Context(), t.Context(), client, NewAuth("", "api-key", client), model, []byte(`{}`))
			switch outcome {
			case "build-fast":
				if !errors.Is(err, ErrBuildFastRequiresOAuth) || calls != 0 {
					t.Fatalf("Build Fast restriction: calls=%d err=%v", calls, err)
				}
			case "send-error":
				_, sendFailure := errors.AsType[*httptransport.SendError](err)
				if !errors.Is(err, cause) || !sendFailure || calls != 1 {
					t.Fatalf("initial send cause: calls=%d err=%v", calls, err)
				}
			default:
				if err != nil || response == nil || calls != 1 || redirects != 0 {
					t.Fatalf("unexpected retry/redirect: calls=%d redirects=%d err=%v", calls, redirects, err)
				}
				response.Body.Close()
			}
			probe, err := client.Get("https://probe.invalid/start")
			if err != nil {
				t.Fatal(err)
			}
			probe.Body.Close()
			if redirects != 1 || probe.StatusCode != http.StatusOK || client.Timeout != time.Second {
				t.Fatal("Forward mutated the supplied client")
			}
		})
	}
}
