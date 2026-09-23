package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type stockExecutionCatalog struct {
	codeMode *codeModeExecutionOwner
	native   bool
}

type codeModeExecutionOwner struct {
	group     *responsesAdditionalTools
	section   *responsesToolSection
	toolIndex int
	name      string
}

// prepareStockExecution recognizes the stock Codex execution interfaces and
// adds only Mekugi's journal guidance to the authoritative Code Mode exec
// description. It never removes, replaces, or rewrites apply_patch,
// exec_command, or the JavaScript execution contract.
func prepareStockExecution(fields map[string]json.RawMessage, catalog *responsesToolCatalog) (stockExecutionCatalog, error) {
	if catalog.top.present && catalog.top.err != nil {
		return stockExecutionCatalog{}, fmt.Errorf("decode responses tools: %w", catalog.top.err)
	}
	var result stockExecutionCatalog
	seenApplyPatch, seenExecCommand := false, false
	claim := func(group *responsesAdditionalTools, section *responsesToolSection, index int) error {
		tool := section.tools[index]
		if tool == nil {
			return nil
		}
		switch tool.Name {
		case "exec":
			if tool.Type != "custom" || !codeModeOwnsStockExecution(tool.Description) {
				return nil
			}
			if result.codeMode != nil {
				return errors.New("responses request defines Code Mode exec more than once")
			}
			result.codeMode = &codeModeExecutionOwner{group: group, section: section, toolIndex: index, name: tool.Name}
		case applyPatchToolName:
			if group != nil || section != catalog.top {
				return nil
			}
			if seenApplyPatch {
				return incompatibleRequest("invalid_tool_catalog", "Native apply_patch is defined more than once. Use one custom apply_patch tool.")
			}
			if tool.Type != "custom" {
				return incompatibleRequest("invalid_tool_catalog", "Native apply_patch must be a custom tool. Check the Codex tool catalog.")
			}
			seenApplyPatch = true
		case nativeExecCommandToolName:
			if group != nil || section != catalog.top {
				return nil
			}
			if seenExecCommand {
				return incompatibleRequest("invalid_tool_catalog", "Native exec_command is defined more than once. Use one function exec_command tool.")
			}
			if tool.Type != "function" {
				return incompatibleRequest("invalid_tool_catalog", "Native exec_command must be a function tool. Check the Codex tool catalog.")
			}
			seenExecCommand = true
		}
		return nil
	}
	for index := range catalog.top.tools {
		if err := claim(nil, catalog.top, index); err != nil {
			return stockExecutionCatalog{}, err
		}
	}
	if catalog.inputObjectsErr == nil {
		for _, group := range catalog.additional {
			if !group.tools.present {
				return stockExecutionCatalog{}, errors.New("decode additional tools: unexpected end of JSON input")
			}
			if group.tools.err != nil {
				return stockExecutionCatalog{}, fmt.Errorf("decode additional tools: %w", group.tools.err)
			}
			for index, tool := range group.tools.tools {
				if tool == nil {
					continue
				}
				if tool.Type == "namespace" && tool.Name == "functions" {
					if tool.nested == nil || tool.nested.err != nil {
						return stockExecutionCatalog{}, errors.New("decode functions tools for stock execution")
					}
					for nestedIndex := range tool.nested.tools {
						if err := claim(group, tool.nested, nestedIndex); err != nil {
							return stockExecutionCatalog{}, err
						}
					}
					continue
				}
				if err := claim(group, group.tools, index); err != nil {
					return stockExecutionCatalog{}, err
				}
			}
		}
	}
	result.native = seenApplyPatch || seenExecCommand
	if result.native && result.codeMode != nil {
		return stockExecutionCatalog{}, incompatibleRequest("invalid_tool_catalog", "The request defines both native and Code Mode execution owners.")
	}
	if result.codeMode != nil {
		owner := result.codeMode
		description, err := injectCodeModeJournalGuidance(owner.section.tools[owner.toolIndex].Description)
		if err != nil {
			return stockExecutionCatalog{}, err
		}
		owner.section.tools[owner.toolIndex].setDescription(description)
		if owner.group == nil {
			if err := catalog.encodeTop(fields); err != nil {
				return stockExecutionCatalog{}, fmt.Errorf("encode Responses tools: %w", err)
			}
		} else if err := catalog.encodeAdditional(fields, owner.group, owner.section); err != nil {
			return stockExecutionCatalog{}, fmt.Errorf("encode Responses input: %w", err)
		}
	}
	return result, nil
}

func codeModeOwnsStockExecution(description string) bool {
	return strings.Contains(description, "tools.exec_command") || strings.Contains(description, "tools.apply_patch")
}
