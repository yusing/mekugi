package router

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const frontendGuidanceStart = "<!-- mekugi-frontends:start -->"
const frontendGuidanceEnd = "<!-- mekugi-frontends:end -->"

//go:embed frontend_guidance.md
var generatedAgentGuidance string

func embeddedFrontendGuide() string {
	before, _, found := strings.Cut(generatedAgentGuidance, frontendGuidanceEnd)
	if !found {
		panic("generated frontend guide is missing its end marker")
	}
	return before + frontendGuidanceEnd
}

func embeddedInstruction(id string) string {
	marker := "\n<instruction id=\"" + id + "\">\n"
	_, after, found := strings.Cut(generatedAgentGuidance, marker)
	if !found {
		panic("generated instruction is missing: " + id)
	}
	before, _, found := strings.Cut(after, "\n</instruction>")
	if !found {
		panic("generated instruction is unclosed: " + id)
	}
	return strings.TrimSpace(before)
}

func standaloneFrontendDescription(name string) string {
	marker := "\n<tool name=\"" + name + "\">\n"
	_, after, found := strings.Cut(embeddedFrontendGuide(), marker)
	if !found {
		return ""
	}
	before, _, _ := strings.Cut(after, "\n</tool>")
	return strings.TrimSpace(before)
}

func frontendGuidanceFromRegistry(contributions []toolContribution) (string, error) {
	var additions strings.Builder
	for _, contribution := range contributions {
		if !contribution.Executable || len(contribution.Specification) == 0 {
			continue
		}
		var specification struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(contribution.Specification, &specification); err != nil || specification.Description == "" {
			return "", fmt.Errorf("missing session frontend description for %s", contribution.Name)
		}
		if strings.Contains(specification.Description, frontendGuidanceStart) || strings.Contains(specification.Description, frontendGuidanceEnd) ||
			strings.Contains(specification.Description, codeModeJournalStart) || strings.Contains(specification.Description, codeModeJournalEnd) {
			return "", fmt.Errorf("session frontend %s description contains guidance marker", contribution.Name)
		}
		if pinned := standaloneFrontendDescription(contribution.Name); pinned != "" {
			if pinned != specification.Description {
				return "", fmt.Errorf("generated frontend guidance is stale for %s", contribution.Name)
			}
			continue
		}
		additions.WriteString("\n<tool name=\"")
		additions.WriteString(contribution.Name)
		additions.WriteString("\">\n")
		additions.WriteString(specification.Description)
		additions.WriteString("\n</tool>\n\n")
	}
	guide := embeddedFrontendGuide()
	if additions.Len() == 0 {
		return guide, nil
	}
	return strings.Replace(guide, "\n</mekugi-frontends>", additions.String()+"</mekugi-frontends>", 1), nil
}

func refreshMarkedToolGuidance(description, start, end, guidance string) (string, error) {
	startCount, endCount := strings.Count(description, start), strings.Count(description, end)
	if startCount == 0 && endCount == 0 {
		if strings.TrimSpace(description) == "" {
			return guidance, nil
		}
		return strings.TrimRight(description, "\r\n") + "\n\n" + guidance, nil
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("tool description contains incomplete or duplicate guidance markers")
	}
	startIndex, endIndex := strings.Index(description, start), strings.Index(description, end)
	if startIndex > endIndex {
		return "", errors.New("tool description contains reversed guidance markers")
	}
	endIndex += len(end)
	return description[:startIndex] + guidance + description[endIndex:], nil
}

func injectFrontendGuidance(description, guidance string) (string, error) {
	return refreshMarkedToolGuidance(description, frontendGuidanceStart, frontendGuidanceEnd, guidance)
}

func removeFrontendGuidance(description string) (string, error) {
	startCount, endCount := strings.Count(description, frontendGuidanceStart), strings.Count(description, frontendGuidanceEnd)
	if startCount == 0 && endCount == 0 {
		return description, nil
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("tool description contains incomplete or duplicate guidance markers")
	}
	startIndex, endIndex := strings.Index(description, frontendGuidanceStart), strings.Index(description, frontendGuidanceEnd)
	if startIndex > endIndex {
		return "", errors.New("tool description contains reversed guidance markers")
	}
	endIndex += len(frontendGuidanceEnd)
	return description[:startIndex] + description[endIndex:], nil
}
