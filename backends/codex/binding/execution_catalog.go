package binding

import (
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

var ErrUnsupportedExecutionInterface = errors.New("This model does not expose the required exec interface. Select a model that supports exec.")

// Source: apps/mekugi/internal/router/mekugi_tool_catalog.go:16:134@874a6e86.
type executionOwner struct {
	group     *codexresponses.AdditionalTools
	namespace *codexresponses.Tool
	section   *codexresponses.ToolSection
	toolIndex int
	name      string
	command   bool
}

// PrepareStockExecution recognizes the stock Codex exec interface and
// adds session-helper guidance to the owning execution description. It never
// removes, replaces, or rewrites apply_patch, exec_command, or the JavaScript
// execution contract.
func PrepareStockExecution(fields map[string]jsonv1.RawMessage, catalog *codexresponses.ToolCatalog, frontendGuidance string, guidance *Guidance) (string, error) {
	if catalog.Top.Present && catalog.Top.Err != nil {
		return "", fmt.Errorf("decode responses tools: %w", catalog.Top.Err)
	}
	var result *executionOwner
	var claim func(*codexresponses.AdditionalTools, *codexresponses.ToolSection, int) error
	claim = func(group *codexresponses.AdditionalTools, section *codexresponses.ToolSection, index int) error {
		tool := section.Tools[index]
		if tool == nil {
			return nil
		}
		switch tool.Name() {
		case "functions":
			if tool.Type() != "namespace" {
				return nil
			}
			if tool.Nested == nil || tool.Nested.Err != nil {
				return errors.New("decode functions tools for stock execution")
			}
			for nestedIndex := range tool.Nested.Tools {
				if err := claim(group, tool.Nested, nestedIndex); err != nil {
					return err
				}
			}
			if result != nil && result.section == tool.Nested {
				result.namespace = tool
			}
		case "exec":
			if tool.Type() != "custom" {
				return nil
			}
			stockDescription := tool.Description()
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
			result = &executionOwner{group: group, section: section, toolIndex: index, name: tool.Name(),
				command: strings.Contains(stockDescription, "tools.exec_command") || codexresponses.CodeModeDeclaresExecutionTool(stockDescription, "exec_command")}
		case "apply_patch", "exec_command":
			return ErrUnsupportedExecutionInterface
		}
		return nil
	}
	for index := range catalog.Top.Tools {
		if err := claim(nil, catalog.Top, index); err != nil {
			return "", err
		}
	}
	if catalog.InputError == nil {
		for _, group := range catalog.Additional {
			if !group.Tools.Present {
				return "", errors.New("decode additional tools: unexpected end of JSON input")
			}
			if group.Tools.Err != nil {
				return "", fmt.Errorf("decode additional tools: %w", group.Tools.Err)
			}
			for index := range group.Tools.Tools {
				if err := claim(group, group.Tools, index); err != nil {
					return "", err
				}
			}
		}
	}
	if result != nil {
		owner := result
		description := owner.section.Tools[owner.toolIndex].Description()
		if guidance != nil {
			var err error
			description, err = guidance.ToolDescription(description, frontendGuidance, !owner.command)
			if err != nil {
				return "", err
			}
		}

		owner.section.Tools[owner.toolIndex].SetDescription(description)
		if owner.group == nil {
			if owner.namespace != nil {
				encoded, err := responses.MarshalJSON(owner.section.Tools)
				if err != nil {
					return "", err
				}
				owner.namespace.SetRawField("tools", encoded)
			}
			if err := catalog.EncodeTop(fields); err != nil {
				return "", fmt.Errorf("encode Responses tools: %w", err)
			}
		} else if err := catalog.EncodeAdditional(fields, owner.group, owner.section); err != nil {
			return "", fmt.Errorf("encode Responses input: %w", err)
		}
	}
	if result == nil {
		return "", nil
	}
	return result.name, nil
}

func codeModeOwnsStockExecution(description string) bool {
	return strings.Contains(description, "tools.exec_command") || strings.Contains(description, "tools.apply_patch") ||
		codexresponses.CodeModeDeclaresExecutionTool(description, "apply_patch") || codexresponses.CodeModeDeclaresExecutionTool(description, "exec_command")
}
