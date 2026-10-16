package openai

import (
	"context"
	"io"
	"net/http"
)

// Observation measures actual provider messages before delivery to the consumer.
type Observation interface {
	Message([]byte)
	Finish(error)
	HTTPErrorBody(io.ReadCloser, int, string, string) io.ReadCloser
}

type observation struct{ Observation }

func (o observation) Message(payload []byte) {
	if o.Observation != nil {
		o.Observation.Message(payload)
	}
}
func (o observation) Finish(err error) {
	if o.Observation != nil {
		o.Observation.Finish(err)
	}
}
func (o observation) HTTPErrorBody(body io.ReadCloser, status int, contentType, encoding string) io.ReadCloser {
	if o.Observation != nil {
		return o.Observation.HTTPErrorBody(body, status, contentType, encoding)
	}
	return body
}
func (c *Client) observe(ctx context.Context, payload []byte, headers, responseHeaders http.Header) observation {
	if c.ObserveWebSocket == nil {
		return observation{}
	}
	return observation{c.ObserveWebSocket(ctx, payload, headers, responseHeaders)}
}
