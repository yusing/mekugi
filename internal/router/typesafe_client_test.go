package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const typesafeTestAPIKey = "typesafe-test-secret"

func newTypesafeTestClient(t *testing.T, handler http.Handler) *typesafeClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &typesafeClient{
		httpClient: server.Client(),
		endpoint:   server.URL,
		apiKey:     typesafeTestAPIKey,
		model:      typesafeModel,
	}
}

func TestTypesafeClientAccountsUsageAvailability(t *testing.T) {
	tests := []struct {
		name       string
		usage      string
		wantInput  uint64
		wantOutput uint64
		wantErr    bool
	}{
		{name: "complete", usage: `"usage":{"input_tokens":12,"output_tokens":4}`, wantInput: 12, wantOutput: 4},
		{name: "usage absent", usage: "", wantErr: false},
		{name: "usage null", usage: `"usage":null`, wantErr: false},
		{name: "null counter", usage: `"usage":{"input_tokens":null,"output_tokens":4}`, wantErr: false},
		{name: "partial counters", usage: `"usage":{"input_tokens":9}`, wantErr: false},
		{name: "negative counter", usage: `"usage":{"input_tokens":-1,"output_tokens":4}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := `{"answers":{"r0":{"type":"noul","noul":0.75}}`
			if tt.usage != "" {
				payload += "," + tt.usage
			}
			payload += "}"
			client := newTypesafeTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+typesafeTestAPIKey {
					t.Errorf("missing provider credential header")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, payload)
			}))

			answers, usage, err := client.nouls(t.Context(), map[string]any{"stdout": "fixture"}, map[string]typesafeNoul{
				"r0": {Type: "noul", Instructions: "fixture"},
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("nouls error = %v, wantErr %v", err, tt.wantErr)
			}
			if usage.Requests != 1 || usage.InputTokens != tt.wantInput || usage.OutputTokens != tt.wantOutput {
				t.Fatalf("usage = %+v", usage)
			}
			wantMissing := uint64(0)
			if tt.name != "complete" {
				wantMissing = 1
			}
			if usage.MissingResponses != wantMissing {
				t.Fatalf("missing usage count = %d, want %d", usage.MissingResponses, wantMissing)
			}
			if tt.wantErr {
				if answers != nil {
					t.Fatalf("answers on malformed usage response = %#v", answers)
				}
			} else if answers["r0"] != 0.75 {
				t.Fatalf("answers = %#v", answers)
			}
		})
	}
}

func TestTypesafeClientReturnsUsageForInvalidAnswers(t *testing.T) {
	for _, response := range []string{
		`{"answers":{"r0":{"type":"other","noul":0.5}},"usage":{"input_tokens":21,"output_tokens":8}}`,
		`{"answers":{"r0":{"type":"noul","noul":"bad"}},"usage":{"input_tokens":21,"output_tokens":8}}`,
		`{"usage":{"input_tokens":21,"output_tokens":8},"answers":{"r0":{"type":"noul","noul":"bad"}}}`,
	} {
		client := newTypesafeTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, response)
		}))
		answers, usage, err := client.nouls(t.Context(), nil, map[string]typesafeNoul{
			"r0": {Type: "noul", Instructions: "fixture"},
		})
		if err == nil || answers != nil {
			t.Fatalf("answers=%#v error=%v, want invalid-answer error", answers, err)
		}
		if usage != (typesafeUsage{InputTokens: 21, OutputTokens: 8, Requests: 1}) {
			t.Fatalf("usage was lost with invalid answers: %+v", usage)
		}
	}
}

func TestTypesafeClientRetryAttemptsAndSecretHandling(t *testing.T) {
	var attempts atomic.Int32
	client := newTypesafeTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+typesafeTestAPIKey {
			t.Errorf("missing provider credential header")
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "provider failure")
	}))
	answers, usage, err := client.nouls(t.Context(), nil, map[string]typesafeNoul{
		"r0": {Type: "noul", Instructions: "fixture"},
	})
	if err == nil || answers != nil || attempts.Load() != typesafeMaxAttempts {
		t.Fatalf("answers=%#v attempts=%d error=%v", answers, attempts.Load(), err)
	}
	if usage != (typesafeUsage{Requests: typesafeMaxAttempts, MissingResponses: typesafeMaxAttempts}) {
		t.Fatalf("retry usage = %+v", usage)
	}
	if strings.Contains(err.Error(), typesafeTestAPIKey) {
		t.Fatal("provider credential appeared in retry error")
	}
}

func TestTypesafeClientDoesNotExposeCredentialInHTTPError(t *testing.T) {
	client := newTypesafeTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid request; authorization was Bearer %s", typesafeTestAPIKey)
	}))
	_, usage, err := client.nouls(t.Context(), nil, map[string]typesafeNoul{
		"r0": {Type: "noul", Instructions: "fixture"},
	})
	if err == nil {
		t.Fatal("expected HTTP failure")
	}
	if usage != (typesafeUsage{Requests: 1, MissingResponses: 1}) {
		t.Fatalf("failed-request usage = %+v", usage)
	}
	if strings.Contains(err.Error(), typesafeTestAPIKey) {
		t.Fatal("provider credential appeared in HTTP error")
	}
}

func TestTypesafeUsageAddOverflowIsSticky(t *testing.T) {
	tests := []struct {
		name string
		base typesafeUsage
		next typesafeUsage
	}{
		{name: "input", base: typesafeUsage{InputTokens: ^uint64(0), Requests: 1}, next: typesafeUsage{InputTokens: 1, Requests: 1}},
		{name: "output", base: typesafeUsage{OutputTokens: ^uint64(0)}, next: typesafeUsage{OutputTokens: 1}},
		{name: "requests", base: typesafeUsage{Requests: ^uint64(0)}, next: typesafeUsage{Requests: 1}},
		{name: "missing", base: typesafeUsage{MissingResponses: ^uint64(0)}, next: typesafeUsage{MissingResponses: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.base
			got.add(tt.next)
			if !got.Incomplete {
				t.Fatalf("overflow not recorded: %+v", got)
			}
			want := tt.base
			want.Incomplete = true
			if got != want {
				t.Fatalf("overflow published partial sum: got %+v want %+v", got, want)
			}
			got.add(typesafeUsage{Requests: 5, InputTokens: 5})
			if got != want {
				t.Fatalf("incomplete accounting resumed: got %+v want %+v", got, want)
			}
		})
	}
}
