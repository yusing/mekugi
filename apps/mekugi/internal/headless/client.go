// Package headless owns noninteractive prompt admission and JSONL evidence.
package headless

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// ReadPrompt validates one stdin prompt without changing its text.
func ReadPrompt(ctx context.Context, input io.Reader) (string, error) {
	const maxPrompt = 16 << 20
	type promptResult struct {
		data []byte
		err  error
	}
	promptC := make(chan promptResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, maxPrompt+1))
		promptC <- promptResult{data, err}
	}()
	var prompt string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-promptC:
		if result.err != nil {
			return "", result.err
		}
		if len(result.data) > maxPrompt {
			return "", errors.New("headless prompt exceeds 16 MiB")
		}
		prompt = string(result.data)
		if strings.TrimSpace(prompt) == "" {
			return "", errors.New("headless requires a nonempty prompt on stdin")
		}
	}
	return prompt, nil
}

// Stream serializes family evidence. The runtime calls Observe before reducing
// each envelope; output failure or interactive input cancels the whole family.
type Stream struct {
	mu     sync.Mutex
	output *jsontext.Encoder
	cancel context.CancelFunc
	err    error
}

func NewStream(output io.Writer, cancel context.CancelFunc) *Stream {
	return &Stream{output: jsontext.NewEncoder(output), cancel: cancel}
}

func (s *Stream) Observe(m appserver.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	err := json.MarshalEncode(s.output, &m)
	if err == nil && m.Method != "" && len(m.ID) != 0 {
		err = fmt.Errorf("headless cannot answer host request %s; continue interactively", m.Method)
	}
	if err == nil && (m.Method == "item/started" || m.Method == "item/completed") {
		var event struct {
			Item struct {
				Type      string           `json:"type"`
				Delivery  string           `json:"delivery"`
				Questions []jsontext.Value `json:"questions"`
			} `json:"item"`
		}
		err = json.Unmarshal(m.Params, &event)
		if err == nil && event.Item.Type == "agentMessage" && event.Item.Delivery == "async" && len(event.Item.Questions) > 0 {
			err = errors.New("headless cannot answer pending questions; continue interactively")
		}
	}
	return s.fail(err)
}

// Fail retains the original family failure across cancellation and joined cleanup.
func (s *Stream) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail(err)
}

func (s *Stream) fail(err error) error {
	if s.err == nil && err != nil {
		s.err = err
		s.cancel()
	}
	return s.err
}

func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Complete follows joined runtime shutdown, never a transient host turn boundary.
func (s *Stream) Complete(thread string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	params, err := json.Marshal(map[string]string{"threadId": thread})
	if err == nil {
		err = json.MarshalEncode(s.output, &appserver.Message{Method: "mekugi/headless/completed", Params: params})
	}
	return s.fail(err)
}
