package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/capturer"
)

func TestCorpusUsageFromProductionCapture(t *testing.T) {
	for _, test := range []struct {
		name, usage string
		complete    bool
	}{
		{"missing_fields", `{}`, false},
		{"missing_categories", `{"input_tokens":100,"output_tokens":20}`, false},
		{"complete", `{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":5}}`, true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(test.name+map[bool]string{true: "/SSE", false: "/JSON"}[stream], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "capture.jsonl")
				recorder, err := capturer.New(capturer.Config{Mode: "passthrough", ModelProtocol: "native", Output: path})
				if err != nil {
					t.Fatal(err)
				}
				defer recorder.Close()
				payload := `{"id":"response-usage","status":"completed","output":[],"usage":` + test.usage + `}`
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+payload+"}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, payload)
					}
				}))
				defer upstream.Close()
				provider := captureReplayProvider{client: &http.Client{Transport: recorder.Transport(http.DefaultTransport)}, url: upstream.URL}
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					parsed, err := parseResponsesRequest(body)
					if err != nil {
						t.Error(err)
						return
					}
					if err := executeRequest(r.Context(), r.Context(), parsed, headers, "capture-usage", provider, w, nil, nil, nil, nil); err != nil {
						t.Error(err)
					}
				}))
				initial := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream; fields["model"] = "gpt-6-astra" })
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(initial.originalBody))
				request.Header = headers.Clone()
				handler.ServeHTTP(httptest.NewRecorder(), request)
				if err := recorder.Close(); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				threads := map[string]bool{}
				for _, line := range bytes.Split(data, []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var record struct {
						Boundary, ThreadID string
						Usage              *capturer.ProviderUsage
					}
					var envelope map[string]json.RawMessage
					if err := json.Unmarshal(line, &envelope); err != nil {
						t.Fatal(err)
					}
					record.Boundary = jsonString(envelope, "boundary")
					record.ThreadID = jsonString(envelope, "thread_id")
					if record.Boundary != "provider" {
						continue
					}
					if err := json.Unmarshal(envelope["usage"], &record.Usage); err != nil {
						t.Fatal(err)
					}
					if record.Usage == nil || record.Usage.EvidenceComplete == nil || *record.Usage.EvidenceComplete != test.complete {
						t.Fatalf("producer lost completeness: %+v", record.Usage)
					}
					threads[record.ThreadID] = true
				}
				result, err := capturer.InspectProviderUsage(t.Context(), path, capturer.UsageInspectionFilter{
					Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Model: "*", Threads: threads})
				if err != nil || len(result) != 1 {
					t.Fatalf("inspect capture: %+v %v", result, err)
				}
				for _, usage := range result {
					if test.complete {
						if usage.State != "observed" || usage.Tokens == nil || usage.Tokens.InputTokens != 100 {
							t.Fatalf("%+v", usage)
						}
					} else if usage.State != "incomplete" || usage.Tokens != nil || usage.MissingRecords != 1 {
						t.Fatalf("missing became zero: %+v", usage)
					}
				}
			})
		}
	}
}
