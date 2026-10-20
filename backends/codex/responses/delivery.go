package responses

// Source: internal/router/client.go:124:516@5f1db30f Responses delivery.

import (
	"bufio"
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/libraries/httptransport"
	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const (
	maxUpstreamSniffBytes = 64 << 10
	// MaxResponseBytes bounds a provider message or buffered JSON response.
	MaxResponseBytes = 64 << 20
)

var (
	utf8BOM                   = []byte{0xef, 0xbb, 0xbf}
	ErrTransform              = errors.New("transform upstream response")
	ErrWrite                  = errors.New("write downstream response")
	ErrDownstreamDisconnected = errors.New("downstream disconnected")
)

// Observer receives raw provider payloads and confirmed streaming delivery.
// It does not own the reader, writer or transformer.
type Observer interface {
	Observe(payload []byte, stream bool)
	Delivered(payload []byte)
	ReadEnded(err error, unterminated bool)
}

// CopyBody copies one response without closing its caller-owned reader.
// JSON is staged output: its caller confirms delivery after downstream admission.
func CopyBody(writer io.Writer, reader io.Reader, stream bool, transformer Transformer, observer Observer) (protocol.TerminalState, error) {
	if stream {
		return copySSETransformed(writer, reader, transformer, observer)
	}
	return copyJSONTransformed(writer, reader, transformer, observer)
}

// WriteEvent emits and flushes one synthesized Responses event.
func WriteEvent(writer io.Writer, payload []byte) error {
	_, err := writeSSEEvent(writer, responseSSELines(payload, "\n"), "\n", nil, nil)
	return err
}

// PrepareBody detects SSE while preserving every inspected byte for delivery.
func PrepareBody(response *http.Response, streamRequested bool) (bool, error) {
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return true, nil
	}
	if !streamRequested {
		return false, nil
	}

	probe := bufio.NewReader(io.LimitReader(response.Body, maxUpstreamSniffBytes+1))
	var prefix bytes.Buffer
	defer func() {
		httptransport.RestorePrefix(response, prefix.Bytes(), io.MultiReader(probe, response.Body))
	}()
	firstLine := true
	for {
		line, err := probe.ReadString('\n')
		prefix.WriteString(line)
		if prefix.Len() > maxUpstreamSniffBytes {
			return false, nil
		}
		content, _ := trimSSELineEnding(line)
		if firstLine {
			content = strings.TrimPrefix(content, "\uFEFF")
			firstLine = false
		}
		if content != "" {
			if isSSELine(content) {
				response.Header.Set("Content-Type", "text/event-stream")
				return true, nil
			}
			jsonPrefix := strings.TrimSpace(content)
			if strings.HasPrefix(jsonPrefix, "{") || strings.HasPrefix(jsonPrefix, "[") {
				return false, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && !errors.Is(err, ErrDownstreamDisconnected) {
				return false, nil
			}
			return false, err
		}
	}
}

func isSSELine(line string) bool {
	if strings.HasPrefix(line, ":") {
		return true
	}
	field, _, _ := strings.Cut(line, ":")
	switch field {
	case "data", "event", "id", "retry":
		return true
	default:
		return false
	}
}

func copyJSONTransformed(writer io.Writer, reader io.Reader, transformer Transformer, observer Observer) (protocol.TerminalState, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return protocol.TerminalUnknown, err
	}
	if len(body) > MaxResponseBytes {
		return protocol.TerminalUnknown, fmt.Errorf("upstream JSON response exceeds the router buffer budget")
	}
	if observer != nil {
		observer.Observe(body, false)
	}
	visible := body
	if transformer != nil {
		visible, err = transformer.TransformJSON(body)
		if err != nil {
			return protocol.TerminalUnknown, fmt.Errorf("%w: %w", ErrTransform, err)
		}
		if err := transformer.Finish(false); err != nil {
			return protocol.TerminalUnknown, fmt.Errorf("%w: %w", ErrTransform, err)
		}
	}
	terminalState := protocol.ObserveTerminal(visible, false)
	if _, err = writer.Write(visible); err != nil {
		return protocol.TerminalUnknown, fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return terminalState, nil
}

func copySSETransformed(writer io.Writer, reader io.Reader, transformer Transformer, observer Observer) (protocol.TerminalState, error) {
	buffered := bufio.NewReader(reader)

	if err := consumeOptionalUTF8BOM(buffered); err != nil {
		if observer != nil {
			observer.ReadEnded(err, false)
		}
		return protocol.TerminalUnknown, err
	}
	event := []string{}
	terminalState := protocol.TerminalUnknown
	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			if line == "\n" || line == "\r\n" {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, line, transformer, observer)
				terminalState = protocol.MergeTerminal(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				event = event[:0]
				if eventTerminalState.Terminal() {
					return terminalState, nil
				}
			} else {
				event = append(event, line)
			}
		}
		if err != nil {
			if observer != nil {
				observer.ReadEnded(err, len(event) != 0)
			}
			// A downstream disconnect may wrap EOF, but it is not a clean end
			// of the provider body. Preserve cancellation rather than replacing
			// it with an incomplete-call translation error during Finish.
			if errors.Is(err, io.EOF) && !errors.Is(err, ErrDownstreamDisconnected) {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, "", transformer, observer)
				terminalState = protocol.MergeTerminal(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				if eventTerminalState.Terminal() {
					return terminalState, nil
				}
				if transformer != nil {
					if finishErr := transformer.Finish(true); finishErr != nil {
						return terminalState, fmt.Errorf("%w: %w", ErrTransform, finishErr)
					}
				}
				return terminalState, nil
			}
			return terminalState, err
		}
	}
}

func consumeOptionalUTF8BOM(reader *bufio.Reader) error {
	prefix, err := reader.Peek(len(utf8BOM))
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if bytes.Equal(prefix, utf8BOM) {
		_, err = reader.Discard(len(utf8BOM))
		return err
	}
	return nil
}

func writeSSEEvent(writer io.Writer, lines []string, separator string, transformer Transformer, observer Observer) (protocol.TerminalState, error) {
	if len(lines) == 0 {
		if separator != "" {
			if _, err := io.WriteString(writer, separator); err != nil {
				return protocol.TerminalUnknown, fmt.Errorf("%w: %w", ErrWrite, err)
			}
		}
		if responseWriter, ok := writer.(http.ResponseWriter); ok {
			if err := http.NewResponseController(responseWriter).Flush(); err != nil {
				return protocol.TerminalUnknown, fmt.Errorf("%w: %w", ErrWrite, err)
			}
		}
		return protocol.TerminalUnknown, nil
	}
	payload := SSEPayload(lines)
	terminalState := protocol.ObserveTerminal(payload, true)

	if observer != nil {
		observer.Observe(payload, true)
	}
	visible := [][]byte{payload}
	if transformer != nil && len(payload) > 0 {
		var err error
		visible, err = transformer.TransformSSE(payload)
		if err != nil {
			return terminalState, fmt.Errorf("%w: %w", ErrTransform, err)
		}
	}
	if len(visible) == 0 {
		return terminalState, nil
	}

	var originalEnvelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &originalEnvelope, jsonv1.DefaultOptionsV1())
	for index, eventPayload := range visible {
		eventLines := lines
		eventSeparator := separator
		var transformedEnvelope struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(eventPayload, &transformedEnvelope, jsonv1.DefaultOptionsV1())
		if !bytes.Equal(eventPayload, payload) && (len(visible) > 1 || transformedEnvelope.Type != originalEnvelope.Type) {
			lineEnding := "\n"
			if separator == "\r\n" {
				lineEnding = "\r\n"
			}
			eventLines = responseSSELines(eventPayload, lineEnding)
			if index < len(visible)-1 && eventSeparator == "" {
				eventSeparator = lineEnding
			}
		}
		augmented := encodeSSEEventPayload(eventLines, eventPayload)
		terminalState = protocol.MergeTerminal(terminalState, protocol.ObserveTerminal(eventPayload, true))
		if _, err := io.WriteString(writer, augmented); err != nil {
			return terminalState, fmt.Errorf("%w: %w", ErrWrite, err)
		}
		if eventSeparator != "" {
			if _, err := io.WriteString(writer, eventSeparator); err != nil {
				return terminalState, fmt.Errorf("%w: %w", ErrWrite, err)
			}
		}
	}
	if responseWriter, ok := writer.(http.ResponseWriter); ok {
		if err := http.NewResponseController(responseWriter).Flush(); err != nil {
			return terminalState, fmt.Errorf("%w: %w", ErrWrite, err)
		}
	}
	for _, payload := range visible {
		ConfirmDelivery(transformer, payload)
		if observer != nil {
			observer.Delivered(payload)
		}
	}
	return terminalState, nil
}

// SSEPayload joins data fields without interpreting the event payload.
func SSEPayload(lines []string) []byte {
	parts := []string{}
	for _, line := range lines {
		content, _ := trimSSELineEnding(line)
		if part, ok := strings.CutPrefix(content, "data:"); ok {
			parts = append(parts, strings.TrimPrefix(part, " "))
		}
	}
	return []byte(strings.Join(parts, "\n"))
}

// Synthesized frames need the same event name and data framing as live SSE.
func responseSSELines(payload []byte, ending string) []string {
	var envelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &envelope, jsonv1.DefaultOptionsV1())
	var lines []string
	if envelope.Type != "" {
		lines = append(lines, "event: "+envelope.Type+ending)
	}
	for line := range bytes.SplitSeq(payload, []byte("\n")) {
		lines = append(lines, "data: "+string(line)+ending)
	}
	return lines
}

func encodeSSEEventPayload(lines []string, payload []byte) string {
	if strings.TrimSpace(string(payload)) == "" || strings.TrimSpace(string(payload)) == "[DONE]" {
		return strings.Join(lines, "")
	}

	var result strings.Builder
	replaced := false
	for _, line := range lines {
		content, ending := trimSSELineEnding(line)
		if !strings.HasPrefix(content, "data:") {
			result.WriteString(line)
			continue
		}
		if !replaced {
			replaced = true
			// Each physical payload line needs its own data field. Preserve an
			// unterminated final line, but separate any preceding payload lines.
			first := true
			for part := range bytes.SplitSeq(payload, []byte("\n")) {
				if !first {
					if ending == "" {
						result.WriteByte('\n')
					} else {
						result.WriteString(ending)
					}
				}
				first = false
				result.WriteString("data: ")
				result.Write(part)
			}
			result.WriteString(ending)
		}
	}
	return result.String()
}

func trimSSELineEnding(line string) (string, string) {
	if before, ok := strings.CutSuffix(line, "\r\n"); ok {
		return before, "\r\n"
	}
	if before, ok := strings.CutSuffix(line, "\n"); ok {
		return before, "\n"
	}
	return line, ""
}
