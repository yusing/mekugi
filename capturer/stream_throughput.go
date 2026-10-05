package capturer

import (
	json "encoding/json/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/responses"
	"github.com/yusing/mekugi/internal/tokenizer"
)

// StreamOutputMeter estimates visible model output only. It does not measure
// hidden reasoning or feed provider usage, costs, or the session average.
// Tokenize fixed-size windows, not individual deltas, so event fragmentation
// does not inflate the estimate. Raw text retention and sample work are bounded.
type StreamOutputMeter struct {
	codec        tokenizer.Codec
	pending      string
	tokens       uint64
	items        map[int]uint8 // 1: deltas observed; 2: item completed.
	nonReasoning bool
	lastSample   time.Duration
	disabled     bool
}

const streamTokenWindow = 16 << 10

// Observe returns a positive rate only when a new sample is ready. Reasoning
// alone needs at least one second and 32 estimated tokens. Terminal usage must
// replace this estimate at the caller, without adding it to measured totals.
func (m *StreamOutputMeter) Observe(payload []byte, elapsed time.Duration) float64 {
	if m.disabled || len(payload) > maxObservedResponseBytes {
		return 0
	}
	var event struct {
		Type        responses.Kind `json:"type"`
		OutputIndex int            `json:"output_index"`
		Delta       string         `json:"delta"`
		Item        struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Input     string `json:"input"`
			Content   []struct {
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return 0
	}
	if event.Type.EndsExchange() {
		m.disabled = true
		m.pending, m.items = "", nil
		return 0
	}
	var text string
	reasoning := false
	switch event.Type {
	case responses.OutputTextDelta, responses.FunctionArgumentsDelta, responses.CustomInputDelta:
		text = event.Delta
	case responses.ReasoningTextDelta, "response.reasoning_text.delta":
		text, reasoning = event.Delta, true
	case responses.OutputItemDone:
		if m.items[event.OutputIndex] == 0 {
			switch event.Item.Type {
			case "function_call":
				text = event.Item.Name + event.Item.Arguments
			case "custom_tool_call":
				text = event.Item.Name + event.Item.Input
			case "message":
				for _, part := range event.Item.Content {
					text += part.Text
				}
			case "reasoning":
				reasoning = true
				for _, part := range event.Item.Summary {
					text += part.Text
				}
			}
		}
	default:
		return 0
	}
	if text == "" && event.Type != responses.OutputItemDone {
		return 0
	}
	if m.items == nil {
		m.items = make(map[int]uint8)
	}
	if len(m.items) >= 4096 && m.items[event.OutputIndex] == 0 {
		m.disabled = true
		return 0
	}
	if event.Type == responses.OutputItemDone {
		m.items[event.OutputIndex] = 2
	} else if m.items[event.OutputIndex] != 2 {
		m.items[event.OutputIndex] = 1
	} else {
		return 0
	}
	if text == "" {
		return 0
	}
	m.nonReasoning = m.nonReasoning || !reasoning
	if m.codec == nil {
		var err error
		m.codec, err = tokenizer.New()
		if err != nil {
			m.disabled = true
			return 0
		}
	}
	// Drain whole windows before sampling the tail. Window boundaries depend on
	// decoded text size, not on event or transport boundaries.
	remaining := m.pending + text
	for len(remaining) >= streamTokenWindow {
		end := streamTokenWindow
		for end < len(remaining) && !utf8.RuneStart(remaining[end]) {
			end--
		}
		n, err := m.codec.Count(remaining[:end])
		if err != nil {
			m.disabled = true
			return 0
		}
		m.tokens += uint64(n)
		remaining = remaining[end:]
	}
	m.pending = strings.Clone(remaining)
	if elapsed <= 0 || elapsed-m.lastSample < 250*time.Millisecond {
		return 0
	}
	n, err := m.codec.Count(m.pending)
	if err != nil {
		m.disabled = true
		return 0
	}
	tokens := m.tokens + uint64(n)
	if !m.nonReasoning && (elapsed < time.Second || tokens < 32) {
		return 0
	}
	m.lastSample = elapsed
	return float64(tokens) / elapsed.Seconds()
}
