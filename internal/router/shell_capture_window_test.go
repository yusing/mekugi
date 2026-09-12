package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// legacyHRunLineText preserves the join-then-bound implementation as a
// differential oracle, including its omission and malformed UTF-8 policy.
func legacyHRunLineText(capture *hrunCapture) string {
	if capture.pending.Len() > 0 || (capture.pendingBytes != nil && capture.pendingBytes.size > 0) {
		capture.finishLine()
	}
	value := strings.Join(capture.rows[capture.rowStart:], "") + strings.Join(capture.rows[:capture.rowStart], "")
	if limit := len(capture.buffer); limit > utf8.UTFMax && len(value) > limit {
		capture.omitted = true
		if capture.tail {
			value = value[len(value)-limit:]
		} else {
			value = value[:limit]
		}
		value = trimHRunBoundary(value, capture.tail)
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func TestHRunRowWindowDifferential(t *testing.T) {
	inputs := []string{
		"", "a", "a\nb\nc\nd\n", "é界🙂\né界🙂\nlast",
		"\xff\x80abc\n\xfe\nlast\x80", strings.Repeat("界", 80) + "\nshort\nend",
		strings.Repeat("abc🙂\n", 50),
	}
	for _, tail := range []bool{false, true} {
		for _, limit := range []int{0, 4, 5, 8, 13, 132} {
			for _, lines := range []int{1, 2, 7, 100} {
				for _, chunk := range []int{1, 3, 1024} {
					for i, input := range inputs {
						t.Run(fmt.Sprintf("tail=%t/bytes=%d/lines=%d/chunk=%d/input=%d", tail, limit, lines, chunk, i), func(t *testing.T) {
							current := hrunCapture{maxLines: lines, buffer: make([]byte, limit), tail: tail}
							legacy := hrunCapture{maxLines: lines, buffer: make([]byte, limit), tail: tail}
							for start := 0; start < len(input); start += chunk {
								part := []byte(input[start:min(start+chunk, len(input))])
								current.Write(part)
								legacy.Write(part)
							}
							want := legacyHRunLineText(&legacy)
							if got := current.text(); got != want || current.omitted != legacy.omitted {
								t.Fatalf("got %q omitted=%t; want %q omitted=%t", got, current.omitted, want, legacy.omitted)
							}
						})
					}
				}
			}
		}
	}
}

func BenchmarkHRunRowWindow(b *testing.B) {
	for _, tail := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			b.Run(fmt.Sprintf("tail=%t/legacy=%t", tail, legacy), func(b *testing.B) {
				rows := make([]string, 1024)
				for i := range rows {
					rows[i] = strings.Repeat("x", 4095) + "\n"
				}
				capture := hrunCapture{maxLines: len(rows), rows: rows, rowStart: 511, buffer: make([]byte, 132), tail: tail}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if legacy {
						legacyHRunLineText(&capture)
					} else {
						capture.text()
					}
				}
			})
		}
	}
}

func TestMixedCarrierProjectsOnlyRuntimeState(t *testing.T) {
	state := hpatchResumeState{
		ChangeID: "hp_test", CorrelationID: "private-correlation",
		ReplayDirectory: "/private/replay", Root: "/private/root",
		Source: "private original source", Handle: "Mhandle",
		ExpiresAt: time.Unix(1234, 0).UTC(), Revision: 7,
		Segments: []hpatchResumeSegment{{Kind: "shell", Source: "echo ok"}},
		Progress: map[string]json.RawMessage{"index": json.RawMessage("1")},
	}
	var transform mekugiResponseTransform
	carrier := transform.mixedCarrier(state, "retry", nil)
	configJSON, _, ok := strings.Cut(strings.TrimPrefix(carrier, "const mixedConfig = "), ";\n")
	if !ok {
		t.Fatal("missing config")
	}
	var config struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.State) != 6 {
		t.Fatalf("unexpected state fields: %s", configJSON)
	}
	for _, name := range []string{"change_id", "expires_at", "handle", "progress", "revision", "segments"} {
		if _, ok := config.State[name]; !ok {
			t.Fatalf("missing %s", name)
		}
	}
	var retained hpatchResumeState
	if err := json.Unmarshal(mustMarshalJSON(state), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Source != state.Source || retained.Root != state.Root || retained.ReplayDirectory != state.ReplayDirectory || retained.CorrelationID != state.CorrelationID {
		t.Fatal("durable state lost private recovery fields")
	}
}
