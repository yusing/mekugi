package router

import (
	"bufio"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/yusing/mekugi/internal/chat"
)

func readEndpointSSE(reader io.Reader, d providerDiagnostics, consume func([]byte) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), upstreamJSONBufferBytes)
	var data []string
	budget := 0
	flush := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = nil
		return consume(payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		budget += len(line)
		if budget > upstreamJSONBufferBytes {
			return d.streamError("invalid", fmt.Sprintf("%s response exceeds the router buffer budget", d.name))
		}
		if line == "" {
			done, err := flush()
			if err != nil || done {
				return err
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return forwardCriticalDiagnostic(err)
	}
	if done, err := flush(); err != nil || done {
		return err
	}
	return d.streamError("invalid", fmt.Sprintf("%s stream ended without its terminal event", d.name))
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
func endpointUsage(d providerDiagnostics, input, output, cached, written, reasoning uint64) (*providerUsage, error) {
	for _, count := range []uint64{input, output, cached, written, reasoning} {
		if count > math.MaxInt64 {
			return nil, d.streamError("invalid_json", "invalid JSON in "+d.name+" response stream")
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
