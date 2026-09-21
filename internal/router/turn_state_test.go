package router

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Model the Codex-owned lifecycle: capture the first response's sticky token,
// echo it on continuations, then omit it on a new turn in the same session.
func TestTurnStateRoundTrip(t *testing.T) {
	for _, mode := range []string{"passthrough", "mekugi"} {
		for _, stream := range []bool{false, true} {
			name := mode + "/json"
			if stream {
				name = mode + "/sse"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					step := int(requests.Add(1) - 1)
					defer r.Body.Close()
					_, _ = io.Copy(io.Discard, r.Body)
					want := ""
					if step == 1 {
						want = "opaque-turn-a"
					}
					if got := r.Header.Get("x-codex-turn-state"); got != want {
						t.Errorf("step %d lost or leaked turn state", step)
					}
					if r.Header.Get(codexSessionIDHeader) != "stable-session" {
						t.Error("session affinity changed")
					}
					token := "opaque-turn-a"
					if step == 2 {
						token = "opaque-turn-b"
					}
					w.Header().Set("x-codex-turn-state", token)
					payload := `{"id":"response-test","status":"completed","output":[]}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+payload+"}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, payload)
					}
				}))
				defer upstream.Close()
				provider := newProviderClient(upstream.URL, upstream.Client())
				var proxy *mekugiProxy
				if mode != "passthrough" {
					proxy = newManagedMekugiProxy(t)
				}
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				maps.Copy(headers, codexAuthHeaders())
				firstToken := ""
				for step := range 3 {
					headers.Del("x-codex-turn-state")
					if step == 1 {
						headers.Set("x-codex-turn-state", firstToken)
					}
					parsed := serverRequest(t, func(fields map[string]any) {
						fields["instructions"] = stockModelInstructionsForTest("", "")
						fields["stream"] = stream
					})
					original := bytes.Clone(parsed.originalBody)
					output := httptest.NewRecorder()
					if err := executeRequest(t.Context(), t.Context(), parsed, headers, "stable-session", provider, output, nil, proxy, nil); err != nil {
						t.Fatal(err)
					}
					if step == 0 {
						firstToken = output.Header().Get("x-codex-turn-state")
					}
					want := "opaque-turn-a"
					if step == 2 {
						want = "opaque-turn-b"
					}
					if got := output.Header().Get("x-codex-turn-state"); got != want {
						t.Fatal("provider turn state not returned to Codex")
					}
					if !bytes.Equal(original, parsed.originalBody) {
						t.Fatal("original request bytes changed")
					}
					if !strings.Contains(output.Body.String(), "completed") {
						t.Fatal("response did not complete")
					}
				}
			})
		}
	}
}
