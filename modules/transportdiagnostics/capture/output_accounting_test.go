package capture

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/commentaryid"
)

func TestGeneratedCommentaryChangesTransportButNotUsage(t *testing.T) {
	r, err := new(Service).Open(Config{Mode: "mekugi"})
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
	if snapshot.Transport.ClientResponses.Bytes <= snapshot.Transport.ProviderResponses.Bytes || snapshot.Usage.OutputTokens != 5 || snapshot.Usage.ReasoningTokens != 3 {
		t.Fatalf("transport/usage = %+v / %+v", snapshot.Transport, snapshot.Usage)
	}
}
