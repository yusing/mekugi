package capturer

import (
	"bytes"
	"iter"
)

// sseLines removes only transport framing, preserving field values verbatim.
func sseLines(payload []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
		for len(payload) != 0 {
			end := bytes.IndexAny(payload, "\r\n")
			if end < 0 {
				yield(payload)
				return
			}
			line := payload[:end]
			terminator := payload[end]
			payload = payload[end+1:]
			if terminator == '\r' && len(payload) != 0 && payload[0] == '\n' {
				payload = payload[1:]
			}
			if !yield(line) {
				return
			}
		}
	}
}

// sseData yields each event's joined data fields, including a final unterminated
// event. Callers retain protocol-specific terminal and measurement policies.
func sseData(payload []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		var parts [][]byte
		for line := range sseLines(payload) {
			if len(line) == 0 {
				if len(parts) != 0 && !yield(bytes.Join(parts, []byte{'\n'})) {
					return
				}
				parts = nil
				continue
			}
			field, value, _ := bytes.Cut(line, []byte{':'})
			if bytes.Equal(field, []byte("data")) {
				parts = append(parts, bytes.TrimPrefix(value, []byte{' '}))
			}
		}
		if len(parts) != 0 {
			yield(bytes.Join(parts, []byte{'\n'}))
		}
	}
}
