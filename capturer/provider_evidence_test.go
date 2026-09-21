package capturer

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderEvidenceDistinguishesMissingNullAndZero(t *testing.T) {
	r := diagnosticRecorder(t)
	for _, test := range []struct {
		name, usage, state string
		count              uint64
	}{
		{"missing usage", "", "missing", 0}, {"null usage", `,"usage":null`, "null", 0},
		{"missing details", `,"usage":{}`, "missing", 0}, {"null details", `,"usage":{"input_tokens_details":null}`, "null", 0},
		{"missing count", `,"usage":{"input_tokens_details":{}}`, "missing", 0},
		{"null count", `,"usage":{"input_tokens_details":{"cached_tokens":null}}`, "null", 0},
		{"zero", `,"usage":{"input_tokens_details":{"cached_tokens":0}}`, "present", 0},
		{"positive", `,"usage":{"input_tokens_details":{"cached_tokens":128}}`, "present", 128},
		{"invalid count", `,"usage":{"input_tokens_details":{"cached_tokens":"private text"}}`, "invalid", 0},
		{"negative count", `,"usage":{"input_tokens_details":{"cached_tokens":-1}}`, "invalid", 0},
		{"invalid details", `,"usage":{"input_tokens_details":[]}`, "invalid", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, sse := range []bool{false, true} {
				body := `{"status":"completed","model":"provider-model","output":[]` + test.usage + `}`
				contentType := "application/json"
				if sse {
					contentType = "text/event-stream"
					body = "data: {\"type\":\"response.created\",\"response\":{\"model\":\"earlier-model\",\"usage\":{\"input_tokens_details\":{\"cached_tokens\":999}}}}\n\ndata: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n"
				}
				evidence := providerHeaderEvidence(nil)
				record := captureRecord{Boundary: "provider", ProviderResponse: &evidence}
				observeResponse([]byte(body), contentType, &record, r.codec)
				got := record.ProviderResponse
				if got.Model != "provider-model" || got.CachedTokensState != test.state {
					t.Fatalf("metadata %+v", got)
				}
				if test.state == "present" {
					if got.CachedTokens == nil || *got.CachedTokens != test.count {
						t.Fatal("explicit cached count lost")
					}
				} else if got.CachedTokens != nil {
					t.Fatal("missing telemetry became an explicit zero")
				}
			}
		})
	}
	record := captureRecord{Boundary: "provider", ProviderResponse: &providerResponseEvidence{CachedTokensState: "unavailable"}}
	observeResponse([]byte("data: {\"type\":\"response.created\",\"response\":{\"model\":\"earlier\"}}\n\n"), "text/event-stream", &record, r.codec)
	if record.ProviderResponse.CachedTokensState != "unavailable" {
		t.Fatal("nonterminal stream became measured")
	}
}

func TestProviderEvidenceRetainsAttemptHeadersWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	r, err := New(Config{Mode: "passthrough", Output: path})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	calls := 0
	transport := r.Transport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		header := http.Header{}
		header.Set("x-request-id", []string{"req-first", "req-second"}[calls-1])
		header.Set("openai-model", "header-model")
		header.Set("Authorization", "private credential")
		header.Set("x-codex-turn-state", "private route")
		payload := []byte(`{"status":"completed","model":"response-model","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":1}}`)
		if calls == 1 {
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			_, _ = gz.Write(payload)
			_ = gz.Close()
			payload = compressed.Bytes()
			header.Set("Content-Type", "application/json")
			header.Set("Content-Encoding", "gzip")
		} else {
			payload = append(append([]byte("data: {\"type\":\"response.completed\",\"response\":"), payload...), []byte("}\n\n")...)
			header.Set("Content-Type", "text/event-stream")
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	}))
	handler := r.Handler(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		body, _ := io.ReadAll(incoming.Body)
		for range 2 {
			req, _ := http.NewRequestWithContext(incoming.Context(), "POST", "http://provider/responses", bytes.NewReader(body))
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Error(err)
				return
			}
			if resp.Header.Get("Authorization") != "private credential" {
				t.Error("response headers changed")
			}
			ObserveProviderUsage(incoming.Context(), ProviderUsage{InputTokens: 10, OutputTokens: 1})
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		io.WriteString(w, `{"status":"completed","output":[]}`)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"requested-model","input":[]}`)))
	snapshot := r.snapshot()
	for i, a := range snapshot.Exchanges[0].ProviderAttempts {
		e := a.ProviderResponse
		if e.RequestID != []string{"req-first", "req-second"}[i] || e.Model != "response-model" || e.HeaderModel != "header-model" || e.CachedTokensState != "present" || e.CachedTokens == nil || *e.CachedTokens != 0 {
			t.Fatalf("attempt evidence %+v", e)
		}
		if a.Model != "requested-model" {
			t.Fatal("response model replaced requested model")
		}
	}
	*snapshot.Exchanges[0].ProviderAttempts[0].ProviderResponse.CachedTokens = 99
	if *r.snapshot().Exchanges[0].ProviderAttempts[0].ProviderResponse.CachedTokens != 0 {
		t.Fatal("snapshot aliases metadata")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("private")) {
		t.Fatal("raw secret retained")
	}
	var records []captureRecord
	for line := range bytes.SplitSeq(bytes.TrimSpace(payload), []byte{'\n'}) {
		var record captureRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 3 || records[0].ProviderResponse.RequestID != "req-first" || records[1].ProviderResponse.RequestID != "req-second" || records[2].ProviderResponse != nil {
		t.Fatal("durable attempt metadata incorrect")
	}
	for _, value := range []string{"unsafe\ntext", "text with spaces", strings.Repeat("x", 257), "<script>"} {
		if safeProviderIdentifier(value) != "" {
			t.Fatal("unsafe identifier accepted")
		}
	}
}

func TestProviderEvidenceRejectsMalformedTerminalEnvelope(t *testing.T) {
	r := diagnosticRecorder(t)
	for _, payload := range []string{`null`, `[]`, `"string"`, `{"status":"completed","output":{},"usage":{"input_tokens_details":{"cached_tokens":0}}}`, `{"status":42,"usage":{"input_tokens_details":{"cached_tokens":128}}}`} {
		record := captureRecord{Boundary: "provider", ProviderResponse: &providerResponseEvidence{CachedTokensState: "unavailable"}}
		body := `data: {"type":"response.completed","response":` + payload + "}\n\n"
		observeResponse([]byte(body), "text/event-stream", &record, r.codec)
		if record.ProviderResponse.CachedTokensState != "unavailable" || record.ProviderResponse.CachedTokens != nil {
			t.Fatal("malformed terminal was measured")
		}
	}
}
