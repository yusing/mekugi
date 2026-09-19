package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type codeModeApplyPatchOwner struct {
	group     *responsesAdditionalTools
	section   *responsesToolSection
	toolIndex int
	name      string

	strippedDescription          string
	execCommandParamsDescription string
}

func installedToolNames(tools []*responsesToolDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		names[tool.Name] = struct{}{}
	}
	return names
}

// replaceCodeModeTools rewrites the authoritative Code Mode exec tool, whether
// top-level or in additional_tools, and exposes the router's standalone tools.
func replaceCodeModeTools(fields map[string]json.RawMessage, catalog *responsesToolCatalog, installedTools []*responsesToolDefinition) (string, bool, error) {
	if catalog.top.present {
		if err := catalog.top.err; err != nil {
			return "", false, fmt.Errorf("decode responses tools: %w", err)
		}
	}
	installedNames := installedToolNames(installedTools)
	owner, err := findCodeModeApplyPatch(catalog, installedNames)
	if err != nil || owner == nil {
		return "", false, err
	}
	for index, tool := range catalog.top.tools {
		if owner.group == nil && index == owner.toolIndex {
			continue
		}
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			return "", false, fmt.Errorf("responses request already defines %s", name)
		}
		if name == applyPatchToolName || name == "exec" || name == "functions.exec" {
			return "", false, fmt.Errorf("responses request exposes unsupported top-level %s", name)
		}
	}
	if codeModeToolChoiceRestricted(fields, owner.name) {
		return "", false, incompatibleRequest("restricted_tool_choice", "The forced Code Mode tool choice prevents Mekugi replacement. Use automatic tool choice.")
	}
	if err := exposeStandaloneMekugi(fields, catalog, owner, installedTools); err != nil {
		return "", false, err
	}
	return owner.name, true, nil
}

// replaceNativeTools replaces the native apply_patch definition while retaining
// exec_command as the executor-owned carrier for translated results.
func replaceNativeTools(fields map[string]json.RawMessage, catalog *responsesToolCatalog, installedTools []*responsesToolDefinition) (string, bool, error) {
	if !catalog.top.present || catalog.top.err != nil {
		return "", false, nil //nolint:nilerr // An absent native tool array belongs to another request shape.
	}
	tools := catalog.top.tools
	installedNames := installedToolNames(installedTools)
	applyPatchIndex := -1
	execCommandIndex := -1
	for index, tool := range tools {
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			return "", false, incompatibleRequest("invalid_tool_catalog", fmt.Sprintf("The request already defines %s. Remove the conflicting HPATCH tool definition.", name))
		}
		switch name {
		case applyPatchToolName:
			if applyPatchIndex >= 0 {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native apply_patch is defined more than once. Use one custom apply_patch tool.")
			}
			if tool.Type != "custom" {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native apply_patch must be a custom tool. Check the Codex tool catalog.")
			}
			applyPatchIndex = index
		case nativeExecCommandToolName:
			if execCommandIndex >= 0 {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native exec_command is defined more than once. Use one function exec_command tool.")
			}
			if tool.Type != "function" {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native exec_command must be a function tool. Check the Codex tool catalog.")
			}
			execCommandIndex = index
		case "exec", "functions.exec":
			return "", false, incompatibleRequest("invalid_tool_catalog", fmt.Sprintf("Unsupported top-level %s tool. Use a supported Codex tool catalog.", name))
		}
	}
	if applyPatchIndex < 0 && execCommandIndex >= 0 {
		return "", false, incompatibleRequest("missing_apply_patch", "The request has exec_command but no apply_patch tool. Enable editing tools for this Codex session.")
	}
	if execCommandIndex < 0 && applyPatchIndex >= 0 {
		return "", false, incompatibleRequest("missing_exec_command", "The request has apply_patch but no exec_command carrier. Enable execution tools for this Codex session.")
	}
	if applyPatchIndex < 0 || execCommandIndex < 0 {
		return "", false, nil
	}
	var choice struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields["tool_choice"], &choice) == nil {
		selected := choice.Name
		if selected == applyPatchToolName || selected == nativeExecCommandToolName {
			return "", false, incompatibleRequest("restricted_tool_choice", "The forced native tool choice prevents Mekugi replacement. Use automatic tool choice.")
		}
	}
	catalog.removeTop(applyPatchIndex)
	catalog.appendTop(installedTools)
	if err := catalog.encodeTop(fields); err != nil {
		return "", false, fmt.Errorf("encode native Responses tools: %w", err)
	}
	return nativeExecCommandToolName, true, nil
}

// findCodeModeApplyPatch locates exactly one authoritative Code Mode exec tool.
func findCodeModeApplyPatch(catalog *responsesToolCatalog, installedNames map[string]struct{}) (*codeModeApplyPatchOwner, error) {
	var owner *codeModeApplyPatchOwner
	claim := func(
		group *responsesAdditionalTools,
		section *responsesToolSection,
		toolIndex int,
		nested bool,
	) error {
		tool := section.tools[toolIndex]
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			if nested {
				return fmt.Errorf("responses functions namespace defines %s", name)
			}
			return fmt.Errorf("responses additional_tools item defines direct %s", name)
		}
		if name == applyPatchToolName {
			if nested {
				return errors.New("responses functions namespace exposes direct apply_patch")
			}
			return errors.New("responses additional_tools item exposes unsupported flat apply_patch")
		}
		if name != "exec" {
			return nil
		}
		if owner != nil {
			return errors.New("responses request defines Code Mode exec more than once")
		}
		stripped, found, err := stripCodeModeApplyPatchSection(tool.Description)
		if err != nil {
			return err
		}
		if !found || tool.Type != "custom" {
			return nil
		}
		var execCommandParamsDescription string
		stripped, execCommandParamsDescription, _, err = stripCodeModeExecCommandContract(stripped)
		if err != nil {
			return err
		}
		owner = &codeModeApplyPatchOwner{
			group:                        group,
			section:                      section,
			toolIndex:                    toolIndex,
			name:                         name,
			strippedDescription:          stripped,
			execCommandParamsDescription: execCommandParamsDescription,
		}
		return nil
	}
	if catalog.top.err == nil {
		for index, tool := range catalog.top.tools {
			if tool != nil && tool.Name == "exec" {
				if err := claim(nil, catalog.top, index, false); err != nil {
					return nil, err
				}
			}
		}
	}
	if catalog.inputObjectsErr != nil && catalog.inputItems == nil {
		return owner, nil
	}
	for _, group := range catalog.additional {
		if group.tools.err != nil {
			continue
		}
		for additionalToolIndex, additionalTool := range group.tools.tools {
			name := additionalTool.Name
			if additionalTool.Type != "namespace" {
				if name == "functions.exec" {
					return nil, errors.New("responses additional_tools item exposes unsupported flat functions.exec")
				}
				if err := claim(group, group.tools, additionalToolIndex, false); err != nil {
					return nil, err
				}
				continue
			}
			if name == "exec" || name == "functions.exec" || name == applyPatchToolName {
				return nil, fmt.Errorf("responses additional_tools item exposes unsupported flat %s", name)
			}
			if _, exists := installedNames[name]; exists {
				return nil, fmt.Errorf("responses additional_tools item defines direct %s", name)
			}
			if name != "functions" {
				continue
			}

			if additionalTool.nested == nil || additionalTool.nested.err != nil {
				continue
			}
			for toolIndex := range additionalTool.nested.tools {
				if err := claim(group, additionalTool.nested, toolIndex, true); err != nil {
					return nil, err
				}
			}
		}
	}
	return owner, nil
}

func codeModeToolChoiceRestricted(fields map[string]json.RawMessage, codeToolName string) bool {
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(fields["tool_choice"], &choice) != nil {
		return false
	}
	return choice.Type == "custom" && choice.Name == codeToolName
}

// exposeStandaloneMekugi exposes standalone mekugi tools in the tool catalog.
func exposeStandaloneMekugi(fields map[string]json.RawMessage, catalog *responsesToolCatalog, owner *codeModeApplyPatchOwner, installedTools []*responsesToolDefinition) error {
	owner.section.tools[owner.toolIndex].setDescription(owner.strippedDescription)
	shellIndex := slices.IndexFunc(installedTools, func(tool *responsesToolDefinition) bool {
		return tool.Name == "shell"
	})
	if shellIndex < 0 {
		return errors.New("built-in shell tool is unavailable")
	}
	if owner.execCommandParamsDescription != "" {
		description := strings.TrimRight(installedTools[shellIndex].Description, "\r\n")
		description += "\n\n" + owner.execCommandParamsDescription
		installedTools[shellIndex].setDescription(description)
	}
	if owner.group != nil {
		if err := catalog.encodeAdditional(fields, owner.group, owner.section); err != nil {
			return fmt.Errorf("encode Responses input: %w", err)
		}
	}
	catalog.appendTop(installedTools)
	if err := catalog.encodeTop(fields); err != nil {
		return fmt.Errorf("encode Responses tools: %w", err)
	}
	return nil
}

func (t *mekugiResponseTransform) routesTool(name string) bool {
	if t == nil || t.proxy == nil {
		return false
	}
	contribution, ok := t.proxy.registry.contribution(name)
	return ok && contribution.ModelVisible
}

func customFreeformTool(name, description string) map[string]json.RawMessage {
	type customTool struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalJSON(customTool{
		Type:        "custom",
		Name:        name,
		Description: description,
	}), &fields); err != nil {
		panic(err)
	}
	return fields
}

const (
	codeModeApplyPatchHeading       = "### `apply_patch`"
	codeModeExecCommandHeading      = "### `exec_command`"
	codeModeExecCommandPlainHeading = "### exec_command"
)

type codeModeSectionMatcher func(string) (int, string)

func stripCodeModeSection(description string, findHeading codeModeSectionMatcher, valid func(string) bool, duplicateError string) (string, string, bool, error) {
	start, heading := findHeading(description)
	if start < 0 {
		return description, "", false, nil
	}
	sectionEnd := len(description)
	remaining := description[start+len(heading):]
	for _, nextHeading := range []string{"\n### ", "\n## "} {
		if offset := strings.Index(remaining, nextHeading); offset >= 0 {
			sectionEnd = min(sectionEnd, start+len(heading)+offset+1)
		}
	}
	section := description[start:sectionEnd]
	if valid != nil && !valid(section) {
		return description, "", false, nil
	}
	lineEnding := "\n"
	if strings.Contains(description, "\r\n") {
		lineEnding = "\r\n"
	}
	stripped := strings.TrimRight(description[:start], "\r\n")
	suffix := strings.TrimLeft(description[sectionEnd:], "\r\n")
	if stripped != "" && suffix != "" {
		stripped += lineEnding + lineEnding
	}
	stripped += suffix
	if duplicateStart, _ := findHeading(stripped); duplicateStart >= 0 {
		return "", "", false, errors.New(duplicateError)
	}
	return stripped, section, true, nil
}

// stripCodeModeApplyPatchSection removes the Code Mode apply_patch section from a
// tool description. It also returns that removed section, which is the native
// patch tool definition mekugi displaces: the host pays for one or the other as
// request input, so measuring mekugi's definition cost requires the text it replaced.
func stripCodeModeApplyPatchSection(description string) (string, bool, error) {
	findHeading := func(text string) (int, string) {
		start := strings.Index(text, codeModeApplyPatchHeading)
		if start < 0 || start > 0 && text[start-1] != '\n' {
			return -1, ""
		}
		return start, codeModeApplyPatchHeading
	}
	const declaration = "declare const tools: { apply_patch(input: string): Promise<unknown>; };"
	valid := func(section string) bool {
		return strings.Contains(section, "exec tool declaration:") && strings.Contains(section, declaration)
	}
	stripped, _, found, err := stripCodeModeSection(
		description,
		findHeading,
		valid,
		"responses Code Mode tool defines nested apply_patch more than once",
	)
	return stripped, found, err
}

// stripCodeModeExecCommandSection removes only the nested command-execution
// section. It recognizes app and CLI description bodies without parsing either
// parameter schema. The apply_patch extractor remains an independent contract.
func stripCodeModeExecCommandSection(description string) (string, string, bool, error) {
	findHeading := func(text string) (int, string) {
		best := -1
		matched := ""
		for _, heading := range []string{codeModeExecCommandHeading, codeModeExecCommandPlainHeading} {
			searchFrom := 0
			for searchFrom < len(text) {
				offset := strings.Index(text[searchFrom:], heading)
				if offset < 0 {
					break
				}
				index := searchFrom + offset
				end := index + len(heading)
				for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
					end++
				}
				lineStart := index == 0 || text[index-1] == '\n'
				lineEnd := end == len(text) || text[end] == '\n' || text[end] == '\r'
				if lineStart && lineEnd {
					if best < 0 || index < best {
						best = index
						matched = heading
					}
					break
				}
				searchFrom = index + len(heading)
			}
		}
		return best, matched
	}
	return stripCodeModeSection(
		description,
		findHeading,
		nil,
		"responses Code Mode tool defines nested exec_command more than once",
	)
}

func execCommandParamsDescription(section string) string {
	const heading = "### `#!params`"
	const appMarker = "exec_command(args:"

	if _, after, ok := strings.Cut(section, appMarker); ok {
		rest := strings.TrimLeft(after, " \t")
		end := strings.Index(rest, "}): Promise")
		if !strings.HasPrefix(rest, "{") || end < 0 {
			return ""
		}
		shape := rest[:end+1]
		inside := shape[1 : len(shape)-1]
		cursor := 0
		for {
			cursor += len(inside[cursor:]) - len(strings.TrimLeft(inside[cursor:], " \t\r\n"))
			if !strings.HasPrefix(inside[cursor:], "//") {
				break
			}
			newline := strings.IndexByte(inside[cursor:], '\n')
			if newline < 0 {
				return ""
			}
			cursor += newline + 1
		}
		field := inside[cursor:]
		colon := strings.IndexByte(field, ':')
		semicolon := strings.IndexByte(field, ';')
		if colon < 0 || semicolon < colon || strings.TrimSpace(field[:colon]) != "cmd" {
			return ""
		}
		shape = "{" + inside[cursor+semicolon+1:] + "}"
		if strings.Contains(shape, "exec_command") {
			return ""
		}
		return heading + "\nThe leading `#!params={...}` directive accepts this request-specific JSON object shape. The script body supplies `cmd`, so omit it.\n\n```ts\n" + shape + "\n```"
	}

	normalized := strings.ReplaceAll(section, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	parameters := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) == "Parameters:"
	})
	if parameters < 0 {
		return ""
	}
	kept := make([]string, 0, len(lines)-parameters)
	for _, line := range lines[parameters+1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if strings.HasPrefix(name, "`cmd`") || strings.HasPrefix(name, "cmd:") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return ""
	}
	fields := strings.Join(kept, "\n")
	if strings.Contains(fields, "exec_command") {
		return ""
	}
	return heading + "\nThe leading `#!params={...}` directive accepts a JSON object with these request-specific fields. The script body supplies `cmd`, so omit it.\n\n" + fields
}

// stripCodeModeExecCommandContract removes the command tool section and the
// introductory example from the model-visible Code Mode description. It derives
// a shell-specific parameter description without retaining the nested tool surface.
func stripCodeModeExecCommandContract(description string) (string, string, bool, error) {
	stripped, section, found, err := stripCodeModeExecCommandSection(description)
	if err != nil {
		return "", "", false, err
	}
	if !found {
		if strings.Contains(description, "exec_command") {
			return "", "", false, errors.New("responses Code Mode tool exposes exec_command without an owned section")
		}
		return description, "", false, nil
	}
	const example = " for example `await tools.exec_command(...)`."
	if count := strings.Count(stripped, example); count > 1 {
		return "", "", false, errors.New("responses Code Mode tool references tools.exec_command more than once outside its section")
	} else if count == 1 {
		stripped = strings.Replace(stripped, example, "", 1)
	}
	if strings.Contains(stripped, "exec_command") {
		return "", "", false, errors.New("responses Code Mode tool exposes exec_command outside its owned contract")
	}
	return stripped, execCommandParamsDescription(section), true, nil
}
