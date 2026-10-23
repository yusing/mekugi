package binding

import (
	"errors"
	"fmt"
	"github.com/yusing/mekugi/backends/codex/responses"
	"strings"
)

// ValidateIndependentThreadCatalog rejects live native registrations without removing similarly
// named MCP tools or rewriting historical native calls into callable aliases.
func ValidateIndependentThreadCatalog(request *responses.Request) error {
	catalog := responses.DecodeToolCatalog(request.Fields)
	if catalog.InputError != nil && len(request.Fields["input"]) > 0 && request.Fields["input"].Kind() != '"' {
		return fmt.Errorf("validate independent thread-control catalog: %w", catalog.InputError)
	}
	var check func(*responses.ToolSection) error
	check = func(section *responses.ToolSection) error {
		if section == nil || !section.Present {
			return nil
		}
		if section.Err != nil || !section.Array {
			return errors.New("independent thread-control catalog is malformed")
		}
		for _, tool := range section.Tools {
			if !tool.IsObject() {
				return errors.New("independent thread-control catalog contains a non-object tool")
			}
			name := tool.Name()
			if name == "collaboration" || name == "mekugi_collaboration" || strings.HasPrefix(name, "collaboration.") || strings.HasPrefix(name, "mekugi_collaboration.") {
				return fmt.Errorf("unsupported host catalog: native collaboration tool %q leaked into independent thread control", name)
			}
			if err := check(tool.Nested); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(catalog.Top); err != nil {
		return err
	}
	for _, group := range catalog.Additional {
		if err := check(group.Tools); err != nil {
			return err
		}
	}
	return nil
}
