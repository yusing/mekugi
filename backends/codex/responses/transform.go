package responses

// Source: internal/router/response_transform.go:1:65@5f1db30f ordered transformation.

import "errors"

type Transformer interface {
	TransformJSON([]byte) ([]byte, error)
	TransformSSE([]byte) ([][]byte, error)
	Finish(bool) error
}

type transformerChain struct {
	first  Transformer
	second Transformer
}

func (chain transformerChain) TransformJSON(payload []byte) ([]byte, error) {
	payload, err := chain.first.TransformJSON(payload)
	if err != nil {
		return nil, err
	}
	return chain.second.TransformJSON(payload)
}

func (chain transformerChain) TransformSSE(payload []byte) ([][]byte, error) {
	payloads, err := chain.first.TransformSSE(payload)
	if err != nil {
		return nil, err
	}
	var transformed [][]byte
	for _, current := range payloads {
		visible, err := chain.second.TransformSSE(current)
		if err != nil {
			return nil, err
		}
		transformed = append(transformed, visible...)
	}
	return transformed, nil
}

func (chain transformerChain) Finish(streamEvent bool) error {
	return errors.Join(chain.first.Finish(streamEvent), chain.second.Finish(streamEvent))
}

// Delivery observers run only after the transport writes and flushes an event.
func ConfirmDelivery(transformer Transformer, payload []byte) {
	if observer, ok := transformer.(interface{ Delivered([]byte) }); ok {
		observer.Delivered(payload)
	}
}

func (chain transformerChain) Delivered(payload []byte) {
	ConfirmDelivery(chain.first, payload)
	ConfirmDelivery(chain.second, payload)
}

func Compose(first, second Transformer) Transformer {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	default:
		return transformerChain{first: first, second: second}
	}
}
