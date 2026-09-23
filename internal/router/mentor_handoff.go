package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sync"

	"github.com/yusing/mekugi/internal/responses"
)

// Source: codex-rs/core/src/responses_metadata.rs:325:426. Codex emits both
// values for AgentControl thread spawns and does not emit the header for an
// ordinary new session or fork.
const (
	mentorLeaderModel       = "gpt-6-sol"
	mentorLeaderEffort      = "high"
	mentorInputTokenLimit   = uint64(50_000)
	mentorMinToolCalls      = uint64(3)
	mentorMinMessages       = uint64(2)
	threadSpawnSubagent     = "collab_spawn"
	threadSpawnSubagentKind = "thread_spawn"
)

type mentorHandoff struct {
	mainEnabled     bool
	subagentEnabled bool
	mu              sync.Mutex
	sessions        map[string]mentorSession
}

type mentorSession struct {
	latestInputTokens  uint64
	toolCalls          uint64
	messages           uint64
	awaitingToolResult bool
	complete           bool
}

type mentorRequest struct {
	owner       *mentorHandoff
	threadID    string
	reset       bool
	observation mentorResponseObservation
}

type mentorProgress struct {
	latestInputTokens  uint64
	toolCalls          uint64
	messages           uint64
	awaitingToolResult bool
	complete           bool
	transitioned       bool
}

func newMentorHandoff(mainEnabled, subagentEnabled bool) *mentorHandoff {
	return &mentorHandoff{
		mainEnabled: mainEnabled, subagentEnabled: subagentEnabled,
		sessions: map[string]mentorSession{},
	}
}

func (m *mentorHandoff) prepare(headers http.Header, metadata codexTurnMetadata, metadataValid bool, request *parsedResponsesRequest) (*mentorRequest, error) {
	if m == nil {
		return nil, nil
	}
	isCompaction := metadataValid && metadata.RequestKind == responses.Compaction
	if !isCompaction && !mentorEligibleModel(request.model()) {
		return nil, nil
	}
	threadID := codexThreadID(headers)
	if isThreadSpawnSubagent(headers) {
		if !m.subagentEnabled {
			return nil, nil
		}
		if !metadataValid || metadata.SubagentKind != threadSpawnSubagentKind {
			return nil, errors.New("mentor handoff requires canonical thread-spawn metadata")
		}
		if !metadata.RequestKind.Scheduled() {
			return nil, nil
		}
	} else {
		if !m.mainEnabled {
			return nil, nil
		}
		// Main sessions (including ordinary forks) have no subagent marker.
		for name := range headers {
			if strings.EqualFold(name, openAISubagentHeader) {
				return nil, nil
			}
		}
		if !metadataValid || metadata.SubagentKind != "" || threadID == "" {
			return nil, nil
		}
		if !metadata.RequestKind.Scheduled() {
			return nil, nil
		}
	}
	if threadID == "" {
		return nil, errors.New("mentor handoff requires a Codex thread ID")
	}
	if metadata.RequestKind == responses.Compaction {
		return &mentorRequest{owner: m, threadID: threadID, reset: true}, nil
	}

	m.mu.Lock()
	state, exists := m.sessions[threadID]
	if state.complete {
		m.mu.Unlock()
		return nil, nil
	}

	requestedModel := request.model()
	model, effort := mentorLeaderModel, mentorLeaderEffort
	if requestedModel == "gpt-5.6" || requestedModel == "gpt-5.6-sol" || requestedModel == "gpt-6-sol" {
		model = "gpt-6-astra"
		var reasoning struct {
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(request.fields["reasoning"], &reasoning)
		switch reasoning.Effort {
		case "high":
			effort = "medium"
		case "xhigh":
			effort = "high"
		case "max", "ultra":
			effort = "xhigh"
		default:
			effort = "low"
		}
	}
	if !isThreadSpawnSubagent(headers) && (requestedModel == "gpt-5.6-luna" || requestedModel == "gpt-6-luna") {
		model, effort = "gpt-6-astra", "medium"
	}
	if err := request.setModelAndReasoningEffort(model, effort); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("prepare Mentor Handoff request: %w", err)
	}
	if !exists {
		m.sessions[threadID] = state
	}
	m.mu.Unlock()
	return &mentorRequest{owner: m, threadID: threadID}, nil
}

func mentorEligibleModel(model string) bool {
	return model == "gpt-5.6" || model == "gpt-5.6-sol" || model == "gpt-5.6-luna" || model == "gpt-5.6-terra" || model == "gpt-6-sol" || model == "gpt-6-luna"
}

func isThreadSpawnSubagent(headers http.Header) bool {
	values := []string{}
	for name, headerValues := range headers {
		if strings.EqualFold(name, openAISubagentHeader) {
			values = append(values, headerValues...)
		}
	}
	return len(values) == 1 && values[0] == threadSpawnSubagent
}

func (r *mentorRequest) record(result requestCompletion) mentorProgress {
	if !result.acceptsOutput() && !result.usageObserved {
		return mentorProgress{}
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.reset {
		if result.succeeded() {
			delete(r.owner.sessions, r.threadID)
		}
		return mentorProgress{}
	}
	state := r.owner.sessions[r.threadID]
	wasComplete := state.complete
	wasAwaitingToolResult := state.awaitingToolResult
	state.latestInputTokens = result.usage.InputTokens
	if result.acceptsOutput() {
		state.toolCalls += r.observation.toolCalls
		state.messages += r.observation.messages
	}
	switch {
	case state.latestInputTokens >= mentorInputTokenLimit || state.messages >= mentorMinMessages:
		state.complete = true
	case result.acceptsOutput() && wasAwaitingToolResult:
		state.complete = true
	case state.toolCalls >= mentorMinToolCalls:
		state.awaitingToolResult = true
	}
	if state.complete {
		state.awaitingToolResult = false
	}
	r.owner.sessions[r.threadID] = state
	return mentorProgress{
		latestInputTokens:  state.latestInputTokens,
		toolCalls:          state.toolCalls,
		messages:           state.messages,
		awaitingToolResult: state.awaitingToolResult,
		complete:           state.complete,
		transitioned:       !wasComplete && state.complete,
	}
}

type mentorResponseObservation struct {
	toolCalls         uint64
	messages          uint64
	sawOutputItemDone bool
}

func (o *mentorResponseObservation) outputItemDone(item json.RawMessage) {
	o.sawOutputItemDone = true
	o.observeItems([]json.RawMessage{item})
}

func (o *mentorResponseObservation) completedOutput(items []json.RawMessage) {
	if !o.sawOutputItemDone {
		o.observeItems(items)
	}
}

func (o *mentorResponseObservation) observeItems(items []json.RawMessage) {
	for _, item := range items {
		var output struct {
			Type responses.ItemKind `json:"type"`
			Role string             `json:"role"`
		}
		if json.Unmarshal(item, &output) != nil {
			continue
		}
		switch {
		case output.Type.ToolCall():
			o.toolCalls++
		case output.Type == responses.Message:
			if strings.TrimSpace(output.Role) == "" || output.Role == "assistant" {
				o.messages++
			}
		}
	}
}
