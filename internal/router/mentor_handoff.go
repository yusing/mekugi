package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Source: codex-rs/core/src/responses_metadata.rs:325:426. Codex emits both
// values for AgentControl thread spawns and does not emit the header for an
// ordinary new session or fork.
const (
	mentorLeaderModel       = "gpt-5.6-sol"
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
	if m == nil || !mentorEligibleModel(request.model()) {
		return nil, nil
	}
	if isThreadSpawnSubagent(headers) {
		if !m.subagentEnabled {
			return nil, nil
		}
		if !metadataValid || metadata.SubagentKind != threadSpawnSubagentKind {
			return nil, errors.New("mentor handoff requires canonical thread-spawn metadata")
		}
		if metadata.RequestKind != "turn" {
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
		if !metadataValid || metadata.SubagentKind != "" || metadata.RequestKind != "turn" || codexThreadID(headers) == "" {
			return nil, nil
		}
	}
	threadID := codexThreadID(headers)
	if threadID == "" {
		return nil, errors.New("mentor handoff requires a Codex thread ID")
	}

	m.mu.Lock()
	state, exists := m.sessions[threadID]
	if state.complete {
		m.mu.Unlock()
		return nil, nil
	}

	requestedModel := request.model()
	model, effort := mentorLeaderModel, mentorLeaderEffort
	if requestedModel == "gpt-5.6" || requestedModel == "gpt-5.6-sol" {
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
	if !isThreadSpawnSubagent(headers) && requestedModel == "gpt-5.6-luna" {
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
	return model == "gpt-5.6" || model == "gpt-5.6-sol" || model == "gpt-5.6-luna" || model == "gpt-5.6-terra"
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

func (r *mentorRequest) record(requestInputTokens uint64, includeCompletedOutput bool) mentorProgress {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	state := r.owner.sessions[r.threadID]
	wasComplete := state.complete
	wasAwaitingToolResult := state.awaitingToolResult
	state.latestInputTokens = requestInputTokens
	if includeCompletedOutput {
		state.toolCalls += r.observation.toolCalls
		state.messages += r.observation.messages
	}
	switch {
	case state.latestInputTokens >= mentorInputTokenLimit || state.messages >= mentorMinMessages:
		state.complete = true
	case includeCompletedOutput && wasAwaitingToolResult:
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

func (o *mentorResponseObservation) TransformJSON(payload []byte) ([]byte, error) {
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	o.observeItems(response.Output)
	return payload, nil
}

func (o *mentorResponseObservation) TransformSSE(payload []byte) ([][]byte, error) {
	if string(payload) == "[DONE]" {
		return [][]byte{payload}, nil
	}
	var event struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}
	switch event.Type {
	case "response.output_item.done":
		o.sawOutputItemDone = true
		o.observeItems([]json.RawMessage{event.Item})
	case "response.completed":
		if !o.sawOutputItemDone {
			o.observeItems(event.Response.Output)
		}
	}
	return [][]byte{payload}, nil
}

func (*mentorResponseObservation) Finish(bool) error {
	return nil
}

func (o *mentorResponseObservation) observeItems(items []json.RawMessage) {
	for _, item := range items {
		var output struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(item, &output) != nil {
			continue
		}
		switch output.Type {
		case "custom_tool_call", "function_call":
			o.toolCalls++
		case "message":
			if strings.TrimSpace(output.Role) == "" || output.Role == "assistant" {
				o.messages++
			}
		}
	}
}
