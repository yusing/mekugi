package router

import "errors"

type responseTransformer interface {
	TransformJSON([]byte) ([]byte, error)
	TransformSSE([]byte) ([][]byte, error)
	Finish(bool) error
}

type responseTransformerChain struct {
	first  responseTransformer
	second responseTransformer
}

func (chain responseTransformerChain) TransformJSON(payload []byte) ([]byte, error) {
	payload, err := chain.first.TransformJSON(payload)
	if err != nil {
		return nil, err
	}
	return chain.second.TransformJSON(payload)
}

func (chain responseTransformerChain) TransformSSE(payload []byte) ([][]byte, error) {
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

func (chain responseTransformerChain) Finish(streamEvent bool) error {
	return errors.Join(chain.first.Finish(streamEvent), chain.second.Finish(streamEvent))
}

// Delivery observers run only after the transport writes and flushes an event.
func confirmResponseDelivery(transformer responseTransformer, payload []byte) {
	if observer, ok := transformer.(interface{ Delivered([]byte) }); ok {
		observer.Delivered(payload)
	}
}

func (chain responseTransformerChain) Delivered(payload []byte) {
	confirmResponseDelivery(chain.first, payload)
	confirmResponseDelivery(chain.second, payload)
}

func composeResponseTransformers(first, second responseTransformer) responseTransformer {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	default:
		return responseTransformerChain{first: first, second: second}
	}
}
