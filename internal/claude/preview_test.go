package claude

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestPreviewDecodeEdit(t *testing.T) {
	for _, tc := range []struct {
		name, tool, input string
		final             bool
		want              session.Edit
	}{
		{"write", "Write", `{"file_path":"/not-created.txt","content":"hello\nworld"}`, true, session.Edit{Path: "/not-created.txt", Content: "hello\nworld"}},
		{"empty write", "Write", `{"content":"","file_path":"empty.txt"}`, true, session.Edit{Path: "empty.txt"}},
		{"edit", "Edit", `{"file_path":"edit.txt","old_string":"old","new_string":"new","replace_all":true}`, true, session.Edit{Path: "edit.txt", Old: "old", Content: "new", Replace: true, ReplaceAll: true}},
		{"delete match", "Edit", `{"new_string":"","old_string":"old","file_path":"edit.txt"}`, true, session.Edit{Path: "edit.txt", Old: "old", Replace: true}},
		{"complete input still streaming", "Write", `{"file_path":"stream.txt","content":"done"}`, false, session.Edit{Path: "stream.txt", Content: "done", Partial: true}},
		{"write prefix", "Write", `{"file_path":"stream.txt","content":"hello`, false, session.Edit{Path: "stream.txt", Content: "hello", Partial: true}},
		{"edit prefix", "Edit", `{"file_path":"edit.txt","old_string":"old","new_string":"new`, false, session.Edit{Path: "edit.txt", Old: "old", Content: "new", Replace: true, Partial: true}},
		{"escaped content", "Write", `{"file_path":"escape.txt","content":"quote: \" slash: \\ line:\n tab:\t`, false, session.Edit{Path: "escape.txt", Content: "quote: \" slash: \\ line:\n tab:\t", Partial: true}},
		{"unfinished escape", "Write", `{"file_path":"escape.txt","content":"safe\`, false, session.Edit{Path: "escape.txt", Content: "safe", Partial: true}},
		{"unfinished unicode", "Write", `{"file_path":"escape.txt","content":"safe\u26`, false, session.Edit{Path: "escape.txt", Content: "safe", Partial: true}},
		{"unpaired high surrogate buffered", "Write", `{"file_path":"escape.txt","content":"safe\uD83D`, false, session.Edit{Path: "escape.txt", Content: "safe", Partial: true}},
		{"unfinished surrogate pair", "Write", `{"file_path":"escape.txt","content":"safe\uD83D\uDE`, false, session.Edit{Path: "escape.txt", Content: "safe", Partial: true}},
		{"surrogate pair", "Write", `{"file_path":"escape.txt","content":"safe\uD83D\uDE00`, false, session.Edit{Path: "escape.txt", Content: "safe😀", Partial: true}},
		{"unicode text", "Write", `{"file_path":"unicode.txt","content":"台灣😀`, false, session.Edit{Path: "unicode.txt", Content: "台灣😀", Partial: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeEdit(tc.tool, tc.input, tc.final)
			if got == nil || *got != tc.want {
				t.Fatalf("decodeEdit() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPreviewRejectUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		name, tool, input string
		final             bool
	}{
		{"unknown tool", "Other", `{"file_path":"a","content":"x"}`, true},
		{"missing path", "Write", `{"content":"x"}`, true},
		{"unfinished path", "Write", `{"content":"x","file_path":"a`, false},
		{"unfinished old operand", "Edit", `{"file_path":"a","new_string":"x","old_string":"o`, false},
		{"empty old operand", "Edit", `{"file_path":"a","old_string":"","new_string":"x"}`, true},
		{"unknown content field", "Write", `{"file_path":"a","new_string":"x"}`, true},
		{"wrong case path", "Write", `{"File_path":"a","content":"x"}`, true},
		{"nonstring content", "Write", `{"file_path":"a","content":42}`, true},
		{"null content", "Write", `{"file_path":"a","content":null}`, true},
		{"invalid replace all", "Edit", `{"file_path":"a","old_string":"o","new_string":"n","replace_all":"yes"}`, true},
		{"final incomplete", "Write", `{"file_path":"a","content":"x`, true},
		{"final trailing value", "Write", `{"file_path":"a","content":"x"} {}`, true},
		{"duplicate path final", "Write", `{"file_path":"a","content":"x","file_path":"b"}`, true},
		{"duplicate path streaming", "Write", `{"file_path":"a","content":"x","file_path":"b"}`, false},
		{"duplicate content streaming", "Write", `{"file_path":"a","content":"x","content":"y`, false},
		{"invalid separator streaming", "Write", `{"file_path":"a","content":"x",!`, false},
		{"invalid escape streaming", "Write", `{"file_path":"a","content":"x\q`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeEdit(tc.tool, tc.input, tc.final); got != nil {
				t.Fatalf("unsafe input projected edit: %+v", got)
			}
		})
	}
}

func TestEditFieldsRequiresCompleteOperands(t *testing.T) {
	f := editFields(`{"file_path":"a","old_string":"old","new_string":"prefix`)
	for _, key := range []string{"file_path", "old_string"} {
		if !f[key].complete {
			t.Errorf("%s not complete: %+v", key, f[key])
		}
	}
	if got := f["new_string"]; got.complete || got.text != "prefix" {
		t.Fatalf("partial content = %+v", got)
	}
}

func TestPreviewSplitUnicodeDeltas(t *testing.T) {
	var a adapter
	previewStart(t, &a, "", "unicode", "unicode-call", 0)
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `{"file_path":"unicode.txt","content":"safe台\uD83D`), "unicode-call", "", "unicode.txt", "safe台", true)
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `\uDE`), "unicode-call", "", "unicode.txt", "safe台", true)
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `00"}`), "unicode-call", "", "unicode.txt", "safe台😀", true)
	previewAssertEdit(t, previewFrame(t, &a, "", map[string]any{"type": "content_block_stop", "index": 0}), "unicode-call", "", "unicode.txt", "safe台😀", false)
}

// Exercise native stream framing, not only the accumulator's private methods.
func previewFrame(t *testing.T, a *adapter, parent string, event map[string]any) []session.Event {
	t.Helper()
	frame := map[string]any{"kind": "event", "event": map[string]any{"type": "stream_event", "parent_tool_use_id": parent, "event": event}}
	data, err := json.Marshal(&frame)
	if err != nil {
		t.Fatal(err)
	}
	events, err := a.decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func previewStart(t *testing.T, a *adapter, parent, message, id string, index int) {
	t.Helper()
	previewFrame(t, a, parent, map[string]any{"type": "message_start", "message": map[string]any{"id": message}})
	previewFrame(t, a, parent, map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "Write", "input": map[string]any{}}})
}

func previewDelta(t *testing.T, a *adapter, parent string, index int, input string) []session.Event {
	t.Helper()
	return previewFrame(t, a, parent, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": input}})
}

func previewAssertEdit(t *testing.T, events []session.Event, id, parent, path, content string, partial bool) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one edit", events)
	}
	e := events[0]
	if e.Kind != "edit" || e.ID != id || e.Role != "Write" || e.Caller != parent || e.Edit == nil {
		t.Fatalf("wrong correlated edit event: %+v", e)
	}
	want := session.Edit{Path: path, Content: content, Partial: partial}
	if *e.Edit != want {
		t.Fatalf("edit = %+v, want %+v", e.Edit, want)
	}
}

func TestPreviewNativeCorrelationAndStop(t *testing.T) {
	var a adapter
	previewStart(t, &a, "", "main", "main-0", 0)
	previewStart(t, &a, "", "main", "main-1", 1)
	previewStart(t, &a, "child", "child-message", "child-0", 0)
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `{"file_path":"main.txt","content":"ma`), "main-0", "", "main.txt", "ma", true)
	previewAssertEdit(t, previewDelta(t, &a, "child", 0, `{"file_path":"child.txt","content":"child"}`), "child-0", "child", "child.txt", "child", true)
	previewAssertEdit(t, previewDelta(t, &a, "", 1, `{"file_path":"second.txt","content":"second"}`), "main-1", "", "second.txt", "second", true)
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `in"}`), "main-0", "", "main.txt", "main", true)
	previewAssertEdit(t, previewFrame(t, &a, "", map[string]any{"type": "content_block_stop", "index": 0}), "main-0", "", "main.txt", "main", false)
	if got := previewDelta(t, &a, "", 0, "ignored"); len(got) != 0 {
		t.Fatalf("stopped call revived: %+v", got)
	}
	previewAssertEdit(t, previewFrame(t, &a, "child", map[string]any{"type": "content_block_stop", "index": 0}), "child-0", "child", "child.txt", "child", false)
	// A reused index in a new message cannot inherit the old message's input.
	previewStart(t, &a, "", "next", "next-1", 1)
	previewAssertEdit(t, previewDelta(t, &a, "", 1, `{"file_path":"next.txt","content":"next"}`), "next-1", "", "next.txt", "next", true)
}

func TestPreviewNativeBashCommandPrefixes(t *testing.T) {
	var a adapter
	for _, parent := range []string{"", "child"} {
		previewFrame(t, &a, parent, map[string]any{"type": "message_start", "message": map[string]any{"id": "message-" + parent}})
		previewFrame(t, &a, parent, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "bash-" + parent, "name": "Bash", "input": map[string]any{}}})
		events := previewDelta(t, &a, parent, 0, `{"command":"cat > out.txt <<'EOF'\n台\uD83D`)
		if len(events) != 1 || events[0].ID != "bash-"+parent || events[0].Caller != parent || events[0].CommandInput == nil || events[0].CommandInput.Text != "cat > out.txt <<'EOF'\n台" || events[0].CommandInput.Complete {
			t.Fatalf("native Bash prefix: %+v", events)
		}
		events = previewDelta(t, &a, parent, 0, `\uDE00\nEOF\n"}`)
		if len(events) != 1 || events[0].CommandInput == nil || events[0].CommandInput.Text != "cat > out.txt <<'EOF'\n台😀\nEOF\n" || events[0].CommandInput.Complete {
			t.Fatalf("native Bash complete arguments are still input: %+v", events)
		}
		events = previewFrame(t, &a, parent, map[string]any{"type": "content_block_stop", "index": 0})
		if len(events) != 1 || events[0].CommandInput == nil || !events[0].CommandInput.Complete {
			t.Fatalf("native Bash input boundary: %+v", events)
		}
	}
	for _, input := range []string{`{"command":42}`, `{"command":"ok","command":"other"}`, `{"command":"bad\q`, `{"command":"unfinished`} {
		if got := decodeCommand("Bash", input, true); got != nil {
			t.Fatalf("invalid final input projected a command: %+v", got)
		}
	}
}

func TestPreviewNativeUnknownAndInvalidFrames(t *testing.T) {
	var a adapter
	previewStart(t, &a, "", "message", "call", 0)
	for _, event := range []map[string]any{
		{"type": "unknown", "index": 0},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "unknown", "partial_json": `{"file_path":"a","content":"x"}`}},
		{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"file_path":"a","content":"x"}`}},
	} {
		if events := previewFrame(t, &a, "", event); len(events) != 0 {
			t.Fatalf("unknown event fabricated projection: %+v", events)
		}
	}
	for _, frame := range []string{
		`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","index":"bad"}}}`,
		`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","index":0,"index":1}}}`,
		`{"kind":"event","event":`,
	} {
		events, err := a.decode([]byte(frame))
		if err == nil || len(events) != 0 {
			t.Fatalf("invalid frame returned events=%+v err=%v", events, err)
		}
	}
	previewAssertEdit(t, previewDelta(t, &a, "", 0, `{"file_path":"valid.txt","content":"valid"}`), "call", "", "valid.txt", "valid", true)
}

func TestToolInputByteLimit(t *testing.T) {
	const limit = 256 << 10
	prefix, suffix := `{"file_path":"limit.txt","content":"`, `"}`
	input := prefix + strings.Repeat("a", limit-len(prefix)-len(suffix)) + suffix
	if got := decodeEdit("Write", input, true); got == nil || len(got.Content) != limit-len(prefix)-len(suffix) {
		t.Fatal("input at byte limit was rejected")
	}
	if got := decodeEdit("Write", input+" ", true); got != nil {
		t.Fatal("input above byte limit was projected")
	}
	var a adapter
	previewStart(t, &a, "", "message", "oversize", 0)
	previewDelta(t, &a, "", 0, prefix)
	events := previewDelta(t, &a, "", 0, strings.Repeat("a", limit))
	if len(events) != 1 || events[0].Kind != "notice" {
		t.Fatalf("overflow events = %+v, want notice", events)
	}
	for _, pending := range a.tools {
		if len(pending.input) > limit {
			t.Fatal("overflow retained oversized input")
		}
	}
	if events := previewDelta(t, &a, "", 0, suffix); len(events) != 0 {
		t.Fatalf("overflow call projected edit: %+v", events)
	}
	previewFrame(t, &a, "", map[string]any{"type": "content_block_stop", "index": 0})
	if len(a.tools) != 0 {
		t.Fatalf("completed overflow call still consumes pending capacity: %d", len(a.tools))
	}
}

func TestToolInputPendingBound(t *testing.T) {
	var a adapter
	for i := range 129 {
		previewStart(t, &a, "", "message", fmt.Sprintf("call-%d", i), i)
	}
	if len(a.tools) != 128 {
		t.Fatalf("pending count = %d, want 128", len(a.tools))
	}
	if events := previewDelta(t, &a, "", 128, `{"file_path":"ignored.txt","content":"ignored"}`); len(events) != 0 {
		t.Fatalf("untracked call fabricated edit: %+v", events)
	}
	previewDelta(t, &a, "", 0, `{"file_path":"first.txt","content":"first"}`)
	previewFrame(t, &a, "", map[string]any{"type": "content_block_stop", "index": 0})
	previewStart(t, &a, "", "message", "replacement", 129)
	previewAssertEdit(t, previewDelta(t, &a, "", 129, `{"file_path":"replacement.txt","content":"replacement"}`), "replacement", "", "replacement.txt", "replacement", true)
	if len(a.tools) != 128 {
		t.Fatalf("pending count after reuse = %d, want 128", len(a.tools))
	}
}
