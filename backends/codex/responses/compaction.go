package responses

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: apps/mekugi/internal/router/mekugi_proxy.go:191:263@b9679c93.
func ValidateCompaction(request *Request, metadata TurnMetadata) error {
	var compaction struct {
		Trigger        string `json:"trigger"`
		Reason         string `json:"reason"`
		Implementation string `json:"implementation"`
		Phase          string `json:"phase"`
		Strategy       string `json:"strategy"`
	}
	if err := json.Unmarshal(metadata.Compaction, &compaction, jsonv1.DefaultOptionsV1()); err != nil || slices.ContainsFunc(
		[]string{compaction.Trigger, compaction.Reason, compaction.Implementation, compaction.Phase, compaction.Strategy},
		func(value string) bool { return strings.TrimSpace(value) == "" },
	) {
		return errors.New("mekugi rewrite requires valid compaction metadata")
	}
	if !request.Stream {
		return errors.New("mekugi compaction bypass requires a streaming request")
	}
	v2 := compaction.Implementation == "responses_compaction_v2"

	var tools []jsonv1.RawMessage
	if rawTools, exists := request.Fields["tools"]; exists {
		if err := json.Unmarshal(rawTools, &tools, jsonv1.DefaultOptionsV1()); err != nil {
			return fmt.Errorf("decode compaction tools: %w", err)
		}
	}
	if !v2 && len(tools) != 0 {
		return errors.New("mekugi compaction request cannot expose tools")
	}

	var items []jsonv1.RawMessage
	if err := json.Unmarshal(request.Fields["input"], &items, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decode compaction input: %w", err)
	}
	if len(items) == 0 {
		return errors.New("mekugi compaction request requires nonempty input")
	}
	triggers := 0
	for index, rawItem := range items {
		var item map[string]jsonv1.RawMessage
		if err := json.Unmarshal(rawItem, &item, jsonv1.DefaultOptionsV1()); err != nil || item == nil {
			return errors.New("mekugi compaction request contains a malformed input item")
		}
		if responseevents.StringField(item, "type") == "compaction_trigger" {
			triggers++
			if !v2 || index != len(items)-1 {
				return errors.New("mekugi compaction trigger must end a V2 request")
			}
		}
		if responseevents.StringField(item, "type") != "additional_tools" {
			continue
		}
		var additionalTools []jsonv1.RawMessage
		if err := json.Unmarshal(item["tools"], &additionalTools, jsonv1.DefaultOptionsV1()); err != nil {
			return fmt.Errorf("decode compaction additional tools: %w", err)
		}
		if !v2 && len(additionalTools) != 0 {
			return errors.New("mekugi compaction request cannot expose tools")
		}
	}
	if v2 && triggers != 1 {
		return errors.New("mekugi V2 compaction requires one final trigger")
	}

	var toolChoice string
	if json.Unmarshal(request.Fields["tool_choice"], &toolChoice, jsonv1.DefaultOptionsV1()) != nil || toolChoice != "auto" {
		return errors.New("mekugi compaction request requires automatic tool choice")
	}
	var parallelToolCalls bool
	if err := json.Unmarshal(request.Fields["parallel_tool_calls"], &parallelToolCalls, jsonv1.DefaultOptionsV1()); err != nil || !v2 && parallelToolCalls {
		return errors.New("mekugi compaction request has incompatible parallel tool calls")
	}
	return nil
}
