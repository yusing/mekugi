package responses

// Source: internal/router/provider_stream_decode.go:1:92@5a2082938e797e6e15c374eb6ae135a0753dea24

import (
	"bufio"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/chat"
)

var errSSEBuffer = errors.New("SSE buffer budget exceeded")

type sseEvent struct {
	readErr error
	data    []byte
	final   bool // EOF rather than an empty-line event delimiter.
}

// scanEndpointSSE owns framing only; adapters retain terminal and error policy.
func scanEndpointSSE(reader io.Reader, bufferBytes int) iter.Seq2[sseEvent, error] {
	return func(yield func(sseEvent, error) bool) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), bufferBytes)
		var data []string
		comment, budget := false, 0
		flush := func(final bool) bool {
			if len(data) == 0 && !comment {
				return true
			}
			event := sseEvent{final: final, readErr: scanner.Err()}
			if len(data) != 0 {
				event.data = []byte(strings.Join(data, "\n"))
			}
			data, comment = nil, false
			return yield(event, nil)
		}
		for scanner.Scan() {
			line := scanner.Text()
			budget += len(line)
			if budget > bufferBytes {
				yield(sseEvent{}, errSSEBuffer)
				return
			}
			if line == "" {
				if !flush(false) {
					return
				}
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(value, " "))
			} else if strings.HasPrefix(line, ":") {
				comment = true
			}
		}
		if err := scanner.Err(); err != nil {
			yield(sseEvent{}, err)
			return
		}
		flush(true)
	}
}

func readEndpointSSE(reader io.Reader, d Diagnostics, consume func([]byte) (bool, error)) error {
	for event, err := range scanEndpointSSE(reader, d.BufferBytes) {
		if err == errSSEBuffer {
			return d.streamError("invalid", fmt.Sprintf("%s response exceeds the router buffer budget", d.Name))
		}
		if err != nil {
			return d.ForwardError(err)
		}
		if done, err := consume(event.data); err != nil || done {
			return err
		}
	}
	return d.streamError("invalid", fmt.Sprintf("%s stream ended without its terminal event", d.Name))
}

func providerChunkDelta(delta providerDelta) providerChunk {
	return providerChunk{Choices: []providerChoice{{Delta: delta}}}
}

func providerChunkText(text string, refusal bool) providerChunk {
	if refusal {
		return providerChunkDelta(providerDelta{Refusal: text})
	}
	return providerChunkDelta(providerDelta{Content: text})
}

func providerChunkCall(index int, id, name, arguments string) providerChunk {
	call := providerCallDelta{Index: index, ID: id}
	call.Function.Name, call.Function.Arguments = name, arguments
	return providerChunkDelta(providerDelta{ToolCalls: []providerCallDelta{call}})
}

func providerChunkFinish(reason string) providerChunk {
	return providerChunk{Choices: []providerChoice{{FinishReason: chat.FinishReason(reason)}}}
}

// Keep the signed-count bounds previously enforced by decoding the synthetic
// Chat wire payload. Provider usage can be unsigned; the shared consumer is not.
func endpointUsage(d Diagnostics, input, output, cached, written, reasoning uint64) (*providerUsage, error) {
	for _, count := range []uint64{input, output, cached, written, reasoning} {
		if count > math.MaxInt64 {
			return nil, d.streamError("invalid_json", "invalid JSON in "+d.Name+" response stream")
		}
	}
	usage := &providerUsage{PromptTokens: int64(input), CompletionTokens: int64(output)}
	usage.PromptDetails.CachedTokens, usage.PromptDetails.CacheWriteTokens = int64(cached), int64(written)
	usage.CompletionDetails.ReasoningTokens = int64(reasoning)
	return usage, nil
}

func jsonEquivalent(left, right jsontext.Value) bool {
	left, right = append(jsontext.Value(nil), left...), append(jsontext.Value(nil), right...)
	if left.Canonicalize() != nil || right.Canonicalize() != nil {
		return false
	}
	return string(left) == string(right)
}
