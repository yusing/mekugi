package responses

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeSSEMultilinePayloadPreservesFields(t *testing.T) {
	payload := []byte("{\n  \"type\": \"response.in_progress\"\n}")
	for _, ending := range []string{"\n", "\r\n", ""} {
		t.Run(map[string]string{"\n": "lf", "\r\n": "crlf", "": "unterminated"}[ending], func(t *testing.T) {
			fields := "event: response.in_progress\nid: provider-id\nretry: 1000\n: keep comment\n"
			lines := strings.SplitAfter(fields, "\n")
			lines = append(lines[:len(lines)-1], "data: old"+ending)
			encoded := encodeSSEEventPayload(lines, payload)
			if !strings.HasPrefix(encoded, fields) {
				t.Fatal("non-data fields changed")
			}
			recovered := SSEPayload(strings.SplitAfter(encoded, "\n"))
			if !bytes.Equal(recovered, payload) {
				t.Fatalf("multiline payload changed: %q", recovered)
			}
			if ending == "" && strings.HasSuffix(encoded, "\n") {
				t.Fatal("unterminated frame acquired a final newline")
			}
		})
	}
}

func TestEncodeSSERepeatedDataPreservesInterleavedFields(t *testing.T) {
	lines := []string{"event: update\r\n", "data: old\r\n", ": between\r\n", "data: stale\r\n", "id: retained\r\n"}
	got := encodeSSEEventPayload(lines, []byte("first\nsecond"))
	want := "event: update\r\ndata: first\r\ndata: second\r\n: between\r\nid: retained\r\n"
	if got != want {
		t.Fatalf("framing = %q, want %q", got, want)
	}
	withoutData := []string{"event: update\n", ": heartbeat\n"}
	if got := encodeSSEEventPayload(withoutData, []byte("ignored")); got != strings.Join(withoutData, "") {
		t.Fatalf("metadata-only event changed: %q", got)
	}
}
