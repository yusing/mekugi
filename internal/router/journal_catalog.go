package router

import (
	"encoding/json"
	"errors"
	"slices"
)

func isStockPlanTool(name string) bool {
	return name == "update_plan" || name == "functions.update_plan"
}

// Strip only catalog declarations. Historical update_plan calls are not tool
// declarations and remain byte-equivalent in the visible replay history.
func stripStockPlanTools(fields map[string]json.RawMessage, catalog *responsesToolCatalog) error {
	var choice struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields["tool_choice"], &choice) == nil && isStockPlanTool(choice.Name) {
		return errors.New("update_plan is unavailable in journal sessions; use automatic tool choice")
	}
	var strip func(*responsesToolSection) error
	strip = func(section *responsesToolSection) error {
		if section.err != nil {
			return section.err
		}
		for index := len(section.tools) - 1; index >= 0; index-- {
			tool := section.tools[index]
			if tool == nil {
				continue
			}
			if tool.Type == "function" && isStockPlanTool(tool.Name) {
				section.tools = slices.Delete(section.tools, index, index+1)
				section.nodes = slices.Delete(section.nodes, index, index+1)
				section.rawTools = slices.Delete(section.rawTools, index, index+1)
				continue
			}
			if node := section.nodes[index]; node != nil && node.nested != nil {
				if err := strip(node.nested); err != nil {
					return err
				}
				tool.setRawField("tools", mustMarshalJSON(node.nested.tools))
			}
		}
		return nil
	}
	if err := strip(catalog.top); err != nil {
		return err
	}
	if catalog.top.present {
		if err := catalog.encodeTop(fields); err != nil {
			return err
		}
	}
	for _, group := range catalog.additional {
		if err := strip(group.tools); err != nil {
			return err
		}
		if err := catalog.encodeAdditional(fields, group, group.tools); err != nil {
			return err
		}
	}
	return nil
}
