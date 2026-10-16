package capture

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestStreamDiagnosticsBoundsAndCompletion(t *testing.T) {
	d := &Stream{}
	for i := range 32 + 1 {
		d.Observe([]byte(fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"function_call","id":"item_%02d","call_id":"call_%02d"}}`, i, i)))
	}
	if len(d.Snapshot().PendingCalls) != 32 || !d.Truncated {
		t.Fatal("pending diagnostics are not bounded")
	}
	d.Observe([]byte(`{"type":"response.function_call_arguments.delta","item_id":"item_00","delta":"secret"}`))
	d.Observe([]byte(`{"type":"response.function_call_arguments.done","item_id":"item_00","arguments":"secret"}`))
	if !d.Snapshot().PendingCalls[0].InputDone {
		t.Fatal("input completion missing")
	}
	d.Observe([]byte(`{"type":"response.output_item.done","item":{"id":"item_00"}}`))
	if len(d.Snapshot().PendingCalls) != 31 {
		t.Fatal("completed call retained")
	}
	d.Observe([]byte(`{"type":"secret event"}`))
	if d.LastEvent != "other" {
		t.Fatal("unknown event leaked")
	}
	d.Observe([]byte(`{"type":"codex.response.metadata","headers":{"x-request-id":"req_456","authorization":"secret"}}`))
	d.Observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
	d.CopyStopped("")
	if d.TerminalEvent != "response.completed" || d.CopyStop != "terminal_event" || d.ProviderRequestID != "req_456" {
		t.Fatalf("terminal metadata: %+v", d)
	}
	if strings.Contains(string(responses.MustMarshalJSON(d.Snapshot())), "secret") {
		t.Fatal("content leaked")
	}
	var disabled *Stream
	disabled.Observe([]byte(`{"type":"error"}`))
	disabled.ReadEnded(time.Now(), "upstream_eof", "", -1)
	disabled.CopyStopped("")
}
