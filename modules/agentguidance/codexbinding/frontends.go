package codexbinding

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
)

const frontendGuidanceStart = "<!-- mekugi-frontends:start -->"
const frontendGuidanceEnd = "<!-- mekugi-frontends:end -->"

//go:embed frontends.md
var preamble string

var frontendFramingTag = regexp.MustCompile(`</?(?:tool|mekugi-frontends)[\t\r\n />]`)

func frontendGuide(commands []command.Spec, sections map[string]string) (string, error) {
	if len(commands) == 0 {
		return "", nil
	}
	var output strings.Builder
	output.WriteString(preamble)
	for _, spec := range commands {
		if err := spec.Validate(); err != nil {
			return "", err
		}
		for _, text := range []string{spec.Description, sections[spec.Name]} {
			if strings.Contains(text, frontendGuidanceStart) || strings.Contains(text, frontendGuidanceEnd) {
				return "", fmt.Errorf("session frontend %s description contains guidance marker", spec.Name)
			}
			if frontendFramingTag.MatchString(text) {
				return "", fmt.Errorf("session frontend %s description contains framing tag", spec.Name)
			}
		}
		fmt.Fprintf(&output, "<tool name=\"%s\">\n%s", spec.Name, spec.Description)
		if text := sections[spec.Name]; text != "" {
			output.WriteString("\n" + text)
		}
		output.WriteString("\n</tool>\n\n")
	}
	output.WriteString("</mekugi-frontends>\n" + frontendGuidanceEnd)
	return output.String(), nil
}

// Source: internal/router/frontend_guidance.go:88:125@[7a069269].
// Removal and refresh share validation without changing absent-marker behavior.
func toolDescription(description, guide string, remove bool) (string, error) {
	startCount, endCount := strings.Count(description, frontendGuidanceStart), strings.Count(description, frontendGuidanceEnd)
	if startCount == 0 && endCount == 0 {
		if remove {
			return description, nil
		}
		if strings.TrimSpace(description) == "" {
			return guide, nil
		}
		return strings.TrimRight(description, "\r\n") + "\n\n" + guide, nil
	}
	if startCount != 1 || endCount != 1 {
		return "", errors.New("tool description contains incomplete or duplicate guidance markers")
	}
	startIndex, endIndex := strings.Index(description, frontendGuidanceStart), strings.Index(description, frontendGuidanceEnd)
	if startIndex > endIndex {
		return "", errors.New("tool description contains reversed guidance markers")
	}
	if remove {
		guide = ""
	}
	return description[:startIndex] + guide + description[endIndex+len(frontendGuidanceEnd):], nil
}
