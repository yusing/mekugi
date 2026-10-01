package capturer

import (
	"strings"
	"testing"
)

func TestProviderServiceTierRequiresTerminalEvidence(t *testing.T) {
	r := diagnosticRecorder(t)
	for _, tc := range []struct{ name, field, want string }{
		{"priority", `,"service_tier":"priority"`, "priority"},
		{"default", `,"service_tier":"default"`, "default"},
		{"future tier", `,"service_tier":"new-tier"`, "new-tier"},
		{"absent", "", ""},
		{"null", `,"service_tier":null`, ""},
		{"number", `,"service_tier":123`, ""},
		{"object", `,"service_tier":{"private":"text"}`, ""},
		{"unsafe", `,"service_tier":"private text\n"`, ""},
		{"oversized", `,"service_tier":"` + strings.Repeat("x", 257) + `"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				payload := `{"status":"completed","output":[]` + tc.field + `}`
				kind := "application/json"
				if stream {
					kind = "text/event-stream"
					payload = "data: " + `{"type":"response.created","response":{"service_tier":"auto"}}` + "\n\ndata: " + `{"type":"response.completed","response":` + payload + "}\n\n"
				}
				record := captureRecord{Boundary: "provider", ProviderResponse: &providerResponseEvidence{}}
				observeResponse([]byte(payload), kind, &record, r.codec)
				if record.ProviderResponse.ServiceTier != tc.want || record.CaptureError != "" {
					t.Fatalf("stream=%v evidence=%+v error=%q", stream, record.ProviderResponse, record.CaptureError)
				}
			}
		})
	}
	for _, payload := range []string{
		`data: {"type":"response.created","response":{"service_tier":"priority"}}` + "\n\n",
		`data: {"type":"response.completed","response":{"service_tier":"priority","output":{}}}` + "\n\n",
	} {
		record := captureRecord{Boundary: "provider", ProviderResponse: &providerResponseEvidence{}}
		observeResponse([]byte(payload), "text/event-stream", &record, r.codec)
		if record.ProviderResponse.ServiceTier != "" {
			t.Fatalf("unfinished/malformed response supplied tier: %+v", record.ProviderResponse)
		}
	}
}

func TestChatServiceTierRequiresCompleteProviderResponse(t *testing.T) {
	r := diagnosticRecorder(t)
	for _, tc := range []struct{ name, payload, kind, boundary, want string }{
		{"json", `{"service_tier":"default","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json", "provider", "default"},
		{"stream", "data: " + `{"service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: " + `{"service_tier":"flex","choices":[]}` + "\n\ndata: [DONE]\n\n", "text/event-stream", "provider", "flex"},
		{"omitted later", "data: " + `{"service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: " + `{"choices":[]}` + "\n\ndata: [DONE]\n\n", "text/event-stream", "provider", "priority"},
		{"null later", "data: " + `{"service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: " + `{"service_tier":null,"choices":[]}` + "\n\ndata: [DONE]\n\n", "text/event-stream", "provider", ""},
		{"truncated", "data: " + `{"service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n", "text/event-stream", "provider", ""},
		{"malformed", "data: " + `{"service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: {\n\ndata: [DONE]\n\n", "text/event-stream", "provider", ""},
		{"client boundary", `{"service_tier":"priority","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json", "codex", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := captureRecord{Boundary: tc.boundary, ProviderResponse: &providerResponseEvidence{}}
			observeChatResponse([]byte(tc.payload), tc.kind, &record, r.codec)
			if record.ProviderResponse.ServiceTier != tc.want {
				t.Fatalf("tier = %q, want %q", record.ProviderResponse.ServiceTier, tc.want)
			}
		})
	}
	// Request-side capture gaps do not invalidate observed response metadata.
	record := captureRecord{Boundary: "provider", CaptureError: "missing projected request observation", ProviderResponse: &providerResponseEvidence{}}
	observeChatResponse([]byte(`{"service_tier":"default","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`), "application/json", &record, r.codec)
	if record.ProviderResponse.ServiceTier != "default" {
		t.Fatal("unrelated capture health suppressed returned tier")
	}
}
