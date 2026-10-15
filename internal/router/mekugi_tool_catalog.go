package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

type executionOwner struct {
	group     *responsesAdditionalTools
	namespace *responsesToolDefinition
	section   *responsesToolSection
	toolIndex int
	name      string
	command   bool
}

// prepareStockExecution recognizes the stock Codex exec interface and
// adds session-helper guidance to the owning execution description. It never
// removes, replaces, or rewrites apply_patch, exec_command, or the JavaScript
// execution contract.
func prepareStockExecution(fields map[string]json.RawMessage, catalog *responsesToolCatalog, frontendGuidance string, guidance *binding.Guidance) (*executionOwner, error) {
	if catalog.top.present && catalog.top.err != nil {
		return nil, fmt.Errorf("decode responses tools: %w", catalog.top.err)
	}
	var result *executionOwner
	var claim func(*responsesAdditionalTools, *responsesToolSection, int) error
	claim = func(group *responsesAdditionalTools, section *responsesToolSection, index int) error {
		tool := section.tools[index]
		if tool == nil {
			return nil
		}
		switch tool.Name {
		case "functions":
			if tool.Type != "namespace" {
				return nil
			}
			if tool.nested == nil || tool.nested.err != nil {
				return errors.New("decode functions tools for stock execution")
			}
			for nestedIndex := range tool.nested.tools {
				if err := claim(group, tool.nested, nestedIndex); err != nil {
					return err
				}
			}
			if result != nil && result.section == tool.nested {
				result.namespace = tool
			}
		case "exec":
			if tool.Type != "custom" {
				return nil
			}
			stockDescription := tool.Description
			if guidance != nil {
				var err error
				stockDescription, err = guidance.ToolDescription(stockDescription, "", true)
				if err != nil {
					return err
				}
			}
			if !codeModeOwnsStockExecution(stockDescription) {
				return nil
			}
			if result != nil {
				return errors.New("responses request defines exec more than once")
			}
			result = &executionOwner{group: group, section: section, toolIndex: index, name: tool.Name,
				command: strings.Contains(stockDescription, "tools.exec_command") || codeModeDeclaresExecutionTool(stockDescription, "exec_command")}
		case applyPatchToolName, nativeExecCommandToolName:
			return incompatibleRequest("unsupported_execution_interface", "This model does not expose the required exec interface. Select a model that supports exec.")
		}
		return nil
	}
	for index := range catalog.top.tools {
		if err := claim(nil, catalog.top, index); err != nil {
			return nil, err
		}
	}
	if catalog.inputObjectsErr == nil {
		for _, group := range catalog.additional {
			if !group.tools.present {
				return nil, errors.New("decode additional tools: unexpected end of JSON input")
			}
			if group.tools.err != nil {
				return nil, fmt.Errorf("decode additional tools: %w", group.tools.err)
			}
			for index := range group.tools.tools {
				if err := claim(group, group.tools, index); err != nil {
					return nil, err
				}
			}
		}
	}
	if result != nil {
		owner := result
		description := owner.section.tools[owner.toolIndex].Description
		if guidance != nil {
			var err error
			description, err = guidance.ToolDescription(description, frontendGuidance, !owner.command)
			if err != nil {
				return nil, err
			}
		}

		owner.section.tools[owner.toolIndex].setDescription(description)
		if owner.group == nil {
			if owner.namespace != nil {
				encoded, err := responses.MarshalJSON(owner.section.tools)
				if err != nil {
					return nil, err
				}
				owner.namespace.setRawField("tools", encoded)
			}
			if err := catalog.encodeTop(fields); err != nil {
				return nil, fmt.Errorf("encode Responses tools: %w", err)
			}
		} else if err := catalog.encodeAdditional(fields, owner.group, owner.section); err != nil {
			return nil, fmt.Errorf("encode Responses input: %w", err)
		}
	}
	return result, nil
}

func codeModeOwnsStockExecution(description string) bool {
	return strings.Contains(description, "tools.exec_command") || strings.Contains(description, "tools.apply_patch") ||
		!isExecutionFreeCodeModeDescription(description)
}
