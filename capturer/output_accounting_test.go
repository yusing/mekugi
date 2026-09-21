package capturer

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/commentaryid"
)

func TestModelOutputExcludesGeneratedCommentary(t *testing.T) {
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	// Identical text and phase are still model output when the ID is not in a
	// router-owned namespace. All non-message items remain represented too.
	model := `{"type":"message","id":"msg-provider","phase":"commentary","content":[{"type":"output_text","text":"Tokens: i=20, ci=12, o=5, r=3"}]}`
	tool := `{"type":"function_call","id":"fc-provider","call_id":"call","name":"exec_command","arguments":"{}"}`
	generated := func(prefix string) string {
		return fmt.Sprintf(`{"type":"message","id":%q,"phase":"commentary","content":[{"type":"output_text","text":"Tokens: i=20, ci=12, o=5, r=3"}]}`, prefix+"0123456789abcdef01234567")
	}
	usage := generated(commentaryid.SubagentPrefix)
	operation := generated(commentaryid.OperationPrefix)
	want := "[" + model + "," + tool + "]"
	all := "[" + usage + "," + model + "," + operation + "," + tool + "]"
	done := "data: " + fmt.Sprintf(`{"type":"response.output_item.done","output_index":1,"item":%s}`, tool) + "\n\n" +
		"data: " + fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, model) + "\n\n" +
		// Unindexed synthetic messages must not displace indexed model items.
		"data: " + fmt.Sprintf(`{"type":"response.output_item.done","item":%s}`, usage) + "\n\n" +
		// Also exclude a synthetic item if a sender supplied an output index.
		"data: " + fmt.Sprintf(`{"type":"response.output_item.done","output_index":2,"item":%s}`, operation) + "\n\n"
	cases := map[string]struct{ payload, kind, want string }{
		"JSON":                            {`{"status":"completed","output":` + all + `}`, "application/json", want},
		"SSE full terminal":               {done + `data: {"type":"response.completed","response":{"status":"completed","output":` + all + "}}\n\n", "text/event-stream", want},
		"SSE telemetry-only terminal":     {done + `data: {"type":"response.completed","response":{"status":"completed","output":[` + usage + "]}}\n\n", "text/event-stream", want},
		"SSE empty terminal":              {done + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", "text/event-stream", want},
		"SSE omitted terminal output":     {done + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "text/event-stream", want},
		"SSE null terminal output":        {done + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":null}}\n\n", "text/event-stream", want},
		"SSE incomplete without terminal": {done, "text/event-stream", ""},
		"only generated":                  {`{"status":"completed","output":[` + usage + "," + operation + "]}", "application/json", "[]"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			record := captureRecord{Boundary: "codex", Mode: "mekugi"}
			got := observeResponse([]byte(test.payload), test.kind, &record, r.codec)
			if string(got) != test.want || record.CaptureError != "" {
				t.Fatalf("output=%s, error=%q; want %s", got, record.CaptureError, test.want)
			}
		})
	}
	for _, record := range []captureRecord{{Boundary: "provider", Mode: "mekugi"}, {Boundary: "codex", Mode: "passthrough"}} {
		got := observeResponse([]byte(`{"status":"completed","output":`+all+`}`), "application/json", &record, r.codec)
		if string(got) != all {
			t.Fatalf("unmodified boundary output = %s", got)
		}
	}
}

func TestGeneratedCommentaryChangesTransportButNotSemanticOutputOrUsage(t *testing.T) {
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{captureID: "output", sequence: 1, providerUsage: map[uint64]ProviderUsage{1: {OutputTokens: 5, ReasoningTokens: 3}}}
	item := `{"type":"function_call","id":"tool","call_id":"call","name":"wait","arguments":"{}"}`
	stream := "data: " + fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item) + "\n\n"
	terminal := `data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"
	synthetic := fmt.Sprintf(`{"type":"message","id":%q,"content":[{"type":"output_text","text":%q}]}`, commentaryid.SubagentPrefix+"usage", strings.Repeat("router telemetry ", 100))
	clientTerminal := `data: {"type":"response.completed","response":{"status":"completed","output":[` + synthetic + "]}}\n\n"
	for _, boundary := range []string{"provider", "codex"} {
		body := stream + terminal
		attempt := uint64(1)
		if boundary == "codex" {
			body, attempt = stream+clientTerminal, 0
		}
		r.recordExchange(state, boundary, attempt, time.Now(), []byte(`{"model":"model"}`), observedPayload{content: []byte(body), bytes: uint64(len(body))}, 200, "text/event-stream", "", nil, providerResponseEvidence{})
	}
	snapshot := r.snapshot()
	if snapshot.Semantic.ClientOutputs != snapshot.Semantic.ProviderAttemptOutputs {
		encoded, _ := json.Marshal(snapshot)
		t.Fatalf("synthetic output affected savings: %s", encoded)
	}
	if snapshot.Transport.ClientResponses.Bytes <= snapshot.Transport.ProviderResponses.Bytes || snapshot.Usage.OutputTokens != 5 || snapshot.Usage.ReasoningTokens != 3 {
		t.Fatalf("transport/usage = %+v / %+v", snapshot.Transport, snapshot.Usage)
	}
}

func TestGrokTextMeasurementMatchesAcrossBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			r, err := New(Config{Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			state := &requestState{captureID: "grok-text", sequence: 1}
			text := "Identical Grok output with \"quotes\" and\nanother line."
			encoded, _ := json.Marshal(text)
			provider := `{"choices":[{"message":{"content":` + string(encoded) + `},"finish_reason":"stop"}]}`
			client := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(encoded) + `}]}]}`
			kind := "application/json"
			if stream {
				provider = "data: " + strings.Replace(provider, `"message"`, `"delta"`, 1) + "\n\ndata: [DONE]\n\n"
				client = `data: {"type":"response.completed","response":` + client + "}\n\n"
				kind = "text/event-stream"
			}
			for _, boundary := range []string{"provider", "codex"} {
				body, request, attempt := provider, `{"model":"grok-4.6","messages":[]}`, uint64(1)
				if boundary == "codex" {
					body, request, attempt = client, `{"model":"grok-4.6","input":[]}`, 0
				}
				r.recordExchange(state, boundary, attempt, time.Now(), []byte(request), observedPayload{content: []byte(body), bytes: uint64(len(body))}, 200, kind, "", nil, providerResponseEvidence{})
			}
			report := r.snapshot()
			// Inspect the serialized report consumed by session tooling too.
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var decoded metricsSnapshot
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			want, _ := r.codec.Count(text)
			if len(decoded.Exchanges) != 1 || len(decoded.Exchanges[0].ProviderAttempts) != 1 {
				t.Fatalf("missing client/provider exchange: %s", data)
			}
			exchange := decoded.Exchanges[0]
			expected := payloadMetrics{Bytes: uint64(len(text)), Tokens: uint64(want)}
			if want == 0 || exchange.ClientFinalText != expected || exchange.ProviderAttempts[0].FinalText != expected {
				t.Fatalf("client=%+v provider=%+v want=%+v", exchange.ClientFinalText, exchange.ProviderAttempts[0].FinalText, expected)
			}
		})
	}
}
