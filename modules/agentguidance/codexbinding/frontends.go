package codexbinding

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const frontendGuidanceStart = "<!-- mekugi-frontends:start -->"
const frontendGuidanceEnd = "<!-- mekugi-frontends:end -->"

//go:embed frontends.md
var preamble string

const workflowStart = "<!-- mekugi-workflow:start -->"
const workflowEnd = "<!-- mekugi-workflow:end -->"

//go:embed workflow.md
var workflowPreamble string

var frontendFramingTag = regexp.MustCompile(`</?(?:tool|mekugi-frontends)[\t\r\n />]`)

func frontendGuide(commands []command.Spec, sections map[string]string, childReplySummary bool) (binding.FrontendGuidance, error) {
	if len(commands) == 0 {
		return binding.FrontendGuidance{}, nil
	}
	var output, workflow strings.Builder
	base, childRule, _ := strings.Cut(workflowPreamble, "<!-- child-reply-summary -->")
	workflow.WriteString(workflowStart + "\n" + base)
	if childReplySummary {
		workflow.WriteString(childRule)
	}
	output.WriteString(preamble)
	for _, spec := range commands {
		if err := spec.Validate(); err != nil {
			return binding.FrontendGuidance{}, err
		}
		for _, text := range []string{spec.Description, sections[spec.Name]} {
			if strings.Contains(text, frontendGuidanceStart) || strings.Contains(text, frontendGuidanceEnd) || strings.Contains(text, workflowStart) || strings.Contains(text, workflowEnd) {
				return binding.FrontendGuidance{}, fmt.Errorf("session frontend %s description contains guidance marker", spec.Name)
			}
			if frontendFramingTag.MatchString(text) {
				return binding.FrontendGuidance{}, fmt.Errorf("session frontend %s description contains framing tag", spec.Name)
			}
		}
		fmt.Fprintf(&output, "<tool name=\"%s\">\n%s", spec.Name, spec.Description)
		if text := sections[spec.Name]; text != "" {
			workflow.WriteString("\n" + text + "\n")
		}
		output.WriteString("\n</tool>\n\n")
	}
	output.WriteString("</mekugi-frontends>\n" + frontendGuidanceEnd)
	workflow.WriteString(workflowEnd)
	return binding.FrontendGuidance{Descriptions: output.String(), Workflow: workflow.String()}, nil
}

// Source: internal/router/frontend_guidance.go:88:125@[7a069269].
// Removal and refresh share validation without changing absent-marker behavior.
func toolDescription(description, guide string, remove bool) (string, error) {
	return replaceSection(description, guide, frontendGuidanceStart, frontendGuidanceEnd, remove, true)
}

func baseInstructions(raw json.RawMessage, workflow string, remove bool) (json.RawMessage, error) {
	text, ok := responses.DecodeString(raw)
	if !ok && len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("base instructions must be a string or null")
	}
	updated, err := replaceSection(text, workflow, workflowStart, workflowEnd, remove || workflow == "", false)
	if err != nil {
		return nil, err
	}
	if updated == text {
		return raw, nil
	}
	return responses.MustMarshalJSON(updated), nil
}

func replaceSection(text, section, start, end string, remove, trim bool) (string, error) {
	startCount, endCount := strings.Count(text, start), strings.Count(text, end)
	if startCount == 0 && endCount == 0 {
		if remove {
			return text, nil
		}
		if text == "" {
			return section, nil
		}
		if trim {
			text = strings.TrimRight(text, "\r\n")
		}
		return text + "\n\n" + section, nil
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("guidance contains incomplete or duplicate markers")
	}
	startIndex, endIndex := strings.Index(text, start), strings.Index(text, end)
	if startIndex > endIndex {
		return "", errors.New("guidance contains reversed markers")
	}
	if remove {
		section = ""
	}
	return text[:startIndex] + section + text[endIndex+len(end):], nil
}
