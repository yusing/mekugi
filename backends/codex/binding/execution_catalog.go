package binding

import (
	"errors"
	"fmt"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

var ErrUnsupportedExecutionInterface = errors.New("This model does not expose the required exec interface. Select a model that supports exec.")

// StockExecutionTool recognizes the native Codex exec interface for capture.
// It does not modify the provider-visible catalog.
func StockExecutionTool(catalog *codexresponses.ToolCatalog) (string, error) {
	if catalog.Top.Present && catalog.Top.Err != nil {
		return "", fmt.Errorf("decode responses tools: %w", catalog.Top.Err)
	}
	var result string
	var scan func(*codexresponses.ToolSection) error
	scan = func(section *codexresponses.ToolSection) error {
		for _, tool := range section.Tools {
			if tool == nil {
				continue
			}
			switch tool.Name() {
			case "functions":
				if tool.Type() != "namespace" {
					continue
				}
				if tool.Nested == nil || tool.Nested.Err != nil {
					return errors.New("decode functions tools for stock execution")
				}
				if err := scan(tool.Nested); err != nil {
					return err
				}
			case "exec":
				if tool.Type() == "custom" && codeModeOwnsStockExecution(tool.Description()) {
					if result != "" {
						return errors.New("responses request defines exec more than once")
					}
					result = tool.Name()
				}
			case "apply_patch", "exec_command":
				return ErrUnsupportedExecutionInterface
			}
		}
		return nil
	}
	if err := scan(catalog.Top); err != nil {
		return "", err
	}
	if catalog.InputError == nil {
		for _, section := range catalog.Additional {
			if !section.Present {
				return "", errors.New("decode additional tools: unexpected end of JSON input")
			}
			if section.Err != nil {
				return "", fmt.Errorf("decode additional tools: %w", section.Err)
			}
			if err := scan(section); err != nil {
				return "", err
			}
		}
	}
	return result, nil
}

func codeModeOwnsStockExecution(description string) bool {
	return strings.Contains(description, "tools.exec_command") || strings.Contains(description, "tools.apply_patch") ||
		codexresponses.CodeModeDeclaresExecutionTool(description, "apply_patch") || codexresponses.CodeModeDeclaresExecutionTool(description, "exec_command")
}
