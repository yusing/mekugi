package router

// Source: execution.go:14:205 parsedResponsesRequest and request parsing.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"strings"
)

type parsedResponsesRequest struct {
	originalBody   []byte
	originalFields map[string]json.RawMessage
	fields         map[string]json.RawMessage
	streamResponse bool
	// cachedInput tracks the native boundary during preparation. The provider
	// reconciler replaces it with the confirmed projected prefix before sending.
	cachedInput int
	rebaseInput bool
	toolCatalog *responsesToolCatalog
	// Authorized reset recovery carries the original guidance read order.
	journalGuidanceOrder []string
	// Locally restored summaries are not model response rounds.
	journalRecoveryTexts  []string
	journalGuidanceSkills map[string][]journalGuidanceSkill
}

// responseTools returns the decoded tool catalog, decoding it on first access.
func (r *parsedResponsesRequest) responseTools() *responsesToolCatalog {
	if r.toolCatalog == nil {
		r.toolCatalog = decodeResponsesToolCatalog(r.fields)
	}
	return r.toolCatalog
}

// isExecutionFreeRequest identifies catalogs that need no editing or
// process-execution projection. Other tools and output schemas remain Codex-owned.
func (r *parsedResponsesRequest) isExecutionFreeRequest() bool {
	// Admission precedes explicit instruction omission, which can remove input items.
	catalog := decodeResponsesToolCatalog(r.fields)
	if catalog.inputObjectsErr != nil {
		return false
	}
	var execSeen, waitSeen bool
	var acceptSection func(*responsesToolSection, bool) bool
	acceptSection = func(section *responsesToolSection, namespaces bool) bool {
		if section == nil || section.err != nil {
			return false
		}
		seen := make(map[string]bool)
		for _, tool := range section.tools {
			if tool == nil || tool.fields == nil {
				return false
			}
			if tool.Name == "" || seen[tool.Name] {
				return false
			}
			seen[tool.Name] = true
			if tool.Type == "namespace" {
				if !namespaces || tool.nested == nil || !tool.nested.array || !acceptSection(tool.nested, false) {
					return false
				}
				continue
			}
			if tool.nested != nil || (tool.Type != "function" && tool.Type != "custom") {
				return false
			}
			switch tool.Name {
			case "apply_patch", "exec_command", "functions.exec":
				return false
			case "exec":
				if execSeen || tool.Type != "custom" || !isExecutionFreeCodeModeDescription(tool.Description) {
					return false
				}
				execSeen = true
			case "wait":
				if waitSeen || tool.Type != "function" {
					return false
				}
				waitSeen = true
			}
		}
		return true
	}
	if !acceptSection(catalog.top, true) {
		return false
	}
	for _, group := range catalog.additional {
		if !group.tools.array || !acceptSection(group.tools, true) {
			return false
		}
	}
	return !waitSeen || execSeen
}

// Both clients advertise nested tools through headings; the App also exposes
// TypeScript declarations. Match declarations, not examples such as
// `await tools.exec_command(...)`, and do not depend on the client's preamble.
var codeModeExecutionDeclaration = regexp.MustCompile(`(?:^|[;{])\s*(?:apply_patch|exec_command)\s*\(`)

func isExecutionFreeCodeModeDescription(description string) bool {
	for line := range strings.SplitSeq(description, "\n") {
		heading := strings.Fields(line)
		if len(heading) < 2 || heading[0] != "###" {
			continue
		}
		switch strings.Trim(heading[1], "`") {
		case "apply_patch", "exec_command":
			return false
		}
	}
	return !codeModeExecutionDeclaration.MatchString(description)
}

// setInput updates the request input and re-indexes additional tool groups.
func (r *parsedResponsesRequest) setInput(input json.RawMessage) {
	r.fields["input"] = input
	if r.toolCatalog == nil {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return
	}
	groupIndex := 0
	for itemIndex, rawItem := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) != nil || jsonString(item, "type") != "additional_tools" {
			continue
		}
		if groupIndex >= len(r.toolCatalog.additional) {
			return
		}
		group := r.toolCatalog.additional[groupIndex]
		group.itemIndex = itemIndex
		group.item = item
		groupIndex++
	}
	r.toolCatalog.inputItems = items
}

// incrementalBody removes only the projected prefix already cached upstream.
// Preparation still sees that prefix; HTTP and Chat Completions providers do not use
// the provider's connection-local Responses cache.
func (r parsedResponsesRequest) incrementalBody(body []byte) ([]byte, error) {
	if (r.cachedInput == 0 && !r.rebaseInput) || isChatCompletionsModel(r.model()) {
		return body, nil
	}
	var fields map[string]json.RawMessage
	var input []json.RawMessage
	if json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields["input"], &input) != nil || r.cachedInput > len(input) {
		return nil, errors.New("invalid WebSocket cached input boundary")
	}
	fields["input"] = mustMarshalJSON(input[r.cachedInput:])
	if r.rebaseInput {
		delete(fields, "previous_response_id")
	}
	return marshalProtocolJSON(fields)
}

// model returns the model name from the request.
func (r parsedResponsesRequest) model() string {
	raw, ok := r.fields["model"]
	if !ok {
		return ""
	}
	var model string
	if json.Unmarshal(raw, &model) != nil {
		return ""
	}
	return strings.TrimSpace(model)
}

// modelDescription returns the model name and reasoning effort as a description string.
func (r parsedResponsesRequest) modelDescription() string {
	return strings.TrimSpace(r.model() + " " + r.reasoningEffort())
}

func (r parsedResponsesRequest) reasoningEffort() string {
	var reasoning struct {
		Effort string `json:"effort"`
	}
	if raw, ok := r.fields["reasoning"]; ok {
		_ = json.Unmarshal(raw, &reasoning)
	}
	return strings.TrimSpace(reasoning.Effort)
}

// promptCacheKey returns the prompt cache key from the request.
func (r parsedResponsesRequest) promptCacheKey() string {
	raw, ok := r.fields["prompt_cache_key"]
	if !ok {
		return ""
	}
	var key string
	if json.Unmarshal(raw, &key) != nil || !validCodexCacheKey(key) {
		return ""
	}
	return key
}

// parseResponsesRequest parses and validates a Responses request from raw JSON bytes.
func parseResponsesRequest(body []byte) (parsedResponsesRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var request map[string]json.RawMessage
	if err := decoder.Decode(&request); err != nil {
		return parsedResponsesRequest{}, fmt.Errorf("decode Responses request: %w", err)
	}
	if request == nil {
		return parsedResponsesRequest{}, errors.New("responses request must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return parsedResponsesRequest{}, errors.New("decode Responses request: multiple JSON values")
		}
		return parsedResponsesRequest{}, fmt.Errorf("decode Responses request trailing data: %w", err)
	}

	var streamResponse bool
	_ = json.Unmarshal(request["stream"], &streamResponse)
	var backgroundResponse bool
	_ = json.Unmarshal(request["background"], &backgroundResponse)
	if backgroundResponse {
		return parsedResponsesRequest{}, errors.New("background Responses requests are not supported")
	}
	return parsedResponsesRequest{fields: request, streamResponse: streamResponse, originalBody: body, originalFields: maps.Clone(request)}, nil
}
