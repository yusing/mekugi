package inferencegateway

import (
	"bytes"
	"testing"
)

func TestResponsesWebSocketQueuesCreateWithoutProviderSocket(t *testing.T) {
	s := &responsesWebSocket{ctx: t.Context()}
	create := []byte(`{"type":"response.create","model":"grok-4","input":"next"}`)
	if err := s.control(create); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.queuedCreate, create) {
		t.Fatalf("queued create = %s", s.queuedCreate)
	}
	if err := s.control(create); err == nil {
		t.Fatal("duplicate continuation was accepted")
	}
	s.queuedCreate = nil
	if err := s.control([]byte(`{"type":"response.steer","input":"change"}`)); err == nil {
		t.Fatal("steering without a provider socket was accepted")
	}
}
