package responses

import (
	"bytes"
	"encoding/json/jsontext"
	"sync"

	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// InputPrefix freezes the initial instruction layer, not conversation history or
// provider confirmation. Native authority changes acquire a new applicable
// instruction layer while retaining the context's immutable contributions.
type InputPrefix struct {
	mu            sync.Mutex
	contributions *prefixContributions
	frozen        *frozenPrefix
}

type prefixContributions struct {
	mu    sync.Mutex
	ready bool
	items []jsontext.Value
}

type frozenPrefix struct {
	native       providerHistory
	instructions jsontext.Value
	items        []jsontext.Value
}

// Fork shares immutable values, never the child's later native instruction layer.
func (p *InputPrefix) Fork() *InputPrefix {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.contributions == nil {
		p.contributions = new(prefixContributions)
	}
	return &InputPrefix{contributions: p.contributions, frozen: p.frozen}
}

// Contribute acquires the context's initial additions once. Prewarm does not call
// this method. Failed acquisition leaves the context uninitialized.
func (p *InputPrefix) Contribute(request *Request, create func() ([]jsontext.Value, error)) error {
	p.mu.Lock()
	if p.contributions == nil {
		p.contributions = new(prefixContributions)
	}
	contributions := p.contributions
	p.mu.Unlock()
	contributions.mu.Lock()
	defer contributions.mu.Unlock()
	if !contributions.ready {
		items, err := create()
		if err != nil {
			return err
		}
		for _, item := range items {
			contributions.items = append(contributions.items, bytes.Clone(item))
		}
		contributions.ready = true
	}
	items, suffix, err := splitInitialPrefix(request.Fields["input"])
	if err != nil {
		return err
	}
	request.Fields["input"] = joinInitialPrefix(append(items, contributions.items...), suffix)
	return nil
}

// Freeze runs after all projections and before provider-history reconciliation.
// The original native layer and model determine applicability: permission,
// role and model changes remain authoritative, while a later feature cannot
// rewrite an already prepared prefix for the same native instruction layer.
func (p *InputPrefix) Freeze(request *Request) error {
	if p == nil {
		return nil
	}
	native, _, err := splitInitialPrefix(request.OriginalField("input"))
	if err != nil {
		return err
	}
	instructions := request.OriginalField("instructions")
	present := len(instructions) != 0
	if len(instructions) == 0 {
		instructions = jsontext.Value("null")
	}
	identity, err := (providerHistory{}).append(append([]jsontext.Value{instructions, protocol.MustMarshalJSON(present), protocol.MustMarshalJSON(request.Model())}, native...))
	if err != nil {
		return err
	}
	items, suffix, err := splitInitialPrefix(request.Fields["input"])
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozen == nil || p.frozen.native != identity {
		frozen := &frozenPrefix{native: identity, instructions: bytes.Clone(request.Fields["instructions"])}
		for _, item := range items {
			frozen.items = append(frozen.items, bytes.Clone(item))
		}
		p.frozen = frozen
		return nil
	}
	if len(p.frozen.instructions) != 0 {
		request.Fields["instructions"] = bytes.Clone(p.frozen.instructions)
	} else {
		delete(request.Fields, "instructions")
	}
	request.Fields["input"] = joinInitialPrefix(p.frozen.items, suffix)
	return nil
}

// Only scan the initial layer. The growing conversation is an opaque suffix,
// spliced unchanged rather than decoded and retained as another transcript.
func splitInitialPrefix(input jsontext.Value) ([]jsontext.Value, []byte, error) {
	input = bytes.TrimSpace(input)
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return nil, []byte("]"), nil
	}
	if text, ok := protocol.DecodeString(input); ok {
		item := protocol.MustMarshalJSON(map[string]string{"role": "user", "content": text})
		return nil, append(item, ']'), nil
	}
	if input[0] != '[' {
		return nil, nil, &InvalidRequestError{Message: "initial prefix requires string or array input"}
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(input))
	if _, err := decoder.ReadToken(); err != nil {
		return nil, nil, err
	}
	var items []jsontext.Value
	for decoder.PeekKind() != ']' {
		start := decoder.InputOffset()
		raw, err := decoder.ReadValue()
		if err != nil {
			return nil, nil, err
		}
		item, ok := protocol.DecodeItem(raw)
		if !ok || item.Type != "additional_tools" && ((item.Type != "" && item.Type != "message") || item.Role != "developer" && item.Role != "system") {
			return items, bytes.TrimLeft(input[start:], " \t\r\n,"), nil
		}
		items = append(items, bytes.Clone(raw))
	}
	return items, []byte("]"), nil
}

func joinInitialPrefix(items []jsontext.Value, suffix []byte) jsontext.Value {
	input := []byte{'['}
	for index, item := range items {
		if index != 0 {
			input = append(input, ',')
		}
		input = append(input, item...)
	}
	if len(items) != 0 && len(suffix) != 0 && suffix[0] != ']' {
		input = append(input, ',')
	}
	return append(input, suffix...)
}
