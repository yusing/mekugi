// Source: internal/router/session_title_generate.go:22:146@[475c2206] naming policy.
package naming

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/inference"
)

//go:embed title.md
var instructions string

type Service struct{}

// State belongs to an application logical thread scope, not a module index.
type State struct {
	mu             sync.Mutex
	prompt         string
	started, named bool
}

func (Service) NewThread(name string, hasTurns bool) *State {
	return &State{started: name != "" || hasTurns, named: name != ""}
}

func (s *State) ObserveMetadata(name string, hasTurns bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" || hasTurns {
		s.started, s.named = true, s.named || name != ""
	}
}

func (s *State) NameChanged(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.named = name != ""
	s.started = s.started || s.named
}

func (s *State) AcceptCandidate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.named
}

func (s *State) NeedsPrompt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.started && s.prompt == ""
}

func (s *State) RetainPrompt(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started && s.prompt == "" {
		s.prompt = strings.TrimSpace(prompt)
	}
}

func (s *State) Admit() func(context.Context, inference.Complete) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.prompt == "" {
		return nil
	}
	s.started = true
	prompt := s.prompt
	s.prompt = ""
	return func(ctx context.Context, complete inference.Complete) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if runes := []rune(prompt); len(runes) > 4096 {
			prompt = string(runes[:4096])
		}
		text, err := complete(ctx, inference.Request{Model: "gpt-6-luna", Effort: "medium", Instructions: strings.TrimSpace(instructions), Input: prompt})
		if err != nil {
			return "", err
		}
		name := strings.Join(strings.Fields(text), " ")
		if name == "" {
			return "", errors.New("session title request returned no title")
		}
		return name, nil
	}
}
