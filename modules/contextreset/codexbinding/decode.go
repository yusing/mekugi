// Package codexbinding decodes supplied host history for the inactive loader.
// It registers no runtime binding.
package codexbinding

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
)

const restoredCallPrefix = "call_mekugi_guidance_"

// Decode preserves completion and source provenance without acquiring content.
func Decode(items []map[string]jsonv1.RawMessage, base, workspace string, firstOrder []string, retainedSkills map[string][]guidance.Skill, recoveryTexts ...string) guidance.Input {
	input := guidance.Input{Workspace: workspace, Instructions: instructions(items, base, workspace), FirstReadOrder: firstOrder}
	for _, skill := range userSkills(items) {
		input.SelectedSkills = append(input.SelectedSkills, guidance.SelectedSkill{Skill: skill, Tool: "function_call", Name: "exec_command"})
	}
	completed := make(map[string]bool)
	for _, item := range items {
		if kind := responses.StringField(item, "type"); kind == "function_call_output" || kind == "custom_tool_call_output" {
			completed[responses.StringField(item, "call_id")] = true
		}
	}
	rounds := responseRounds(items, recoveryTexts)
	for _, item := range items {
		id := responses.StringField(item, "call_id")
		if !completed[id] {
			continue
		}
		kind, name := responses.StringField(item, "type"), responses.StringField(item, "name")
		skills, hasSkills := retainedSkills[id]
		observation := guidance.Read{Tool: kind, Name: name, ResponseRound: rounds[id], Restored: strings.HasPrefix(id, restoredCallPrefix), Skills: skills, HasSkillIdentity: hasSkills}
		switch {
		case kind == "custom_tool_call" && name == "exec":
			observation.Commands, _ = commandanalysis.LiteralExecCommands(responses.StringField(item, "input"), workspace, "")
		case kind == "function_call" && name == "exec_command":
			if command, ok := commandanalysis.ExecCommandArguments(responses.StringField(item, "arguments"), workspace, ""); ok {
				observation.Commands = []commandanalysis.Command{command}
			}
		}
		input.CompletedReads = append(input.CompletedReads, observation)
	}
	return input
}

// Source: internal/router/context_reset_guidance.go:79:179@57f4b4c6
// Contiguous model items share a response, including parallel calls. User
// input and tool results separate rounds; restored recovery is not model work.
func responseRounds(items []map[string]jsonv1.RawMessage, recoveryTexts []string) map[string]int {
	rounds := make(map[string]int)
	round, inResponse := 0, false
	for i, item := range items {
		id := responses.StringField(item, "call_id")
		if strings.HasPrefix(id, restoredCallPrefix) {
			continue
		}
		kind, role := responses.StringField(item, "type"), responses.StringField(item, "role")
		if role == "assistant" && slices.Contains(recoveryTexts, strings.TrimSuffix(messageContentText(item["content"]), "\n")) {
			continue
		}
		if role == "assistant" && i+1 < len(items) && strings.HasPrefix(responses.StringField(items[i+1], "call_id"), restoredCallPrefix) {
			continue
		}
		model := role == "assistant" || kind == "reasoning" || kind == "function_call" || kind == "custom_tool_call"
		if model {
			if !inResponse {
				round++
				inResponse = true
			}
			if id != "" && rounds[id] == 0 {
				rounds[id] = round
			}
		} else if role == "user" || kind == "function_call_output" || kind == "custom_tool_call_output" {
			inResponse = false
		}
	}
	return rounds
}

func userSkills(items []map[string]jsonv1.RawMessage) []guidance.Skill {
	var skills []guidance.Skill
	add := func(name, path string) {
		skill := guidance.Skill{Name: name, Path: path}
		if name != "" && !slices.Contains(skills, skill) {
			skills = append(skills, skill)
		}
	}
	for _, item := range items {
		if responses.StringField(item, "role") != "user" {
			continue
		}
		text := messageContentText(item["content"])
		if strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for ") {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item["content"], &parts) != nil {
			parts = append(parts, struct {
				Text string `json:"text"`
			}{text})
		}
		for _, part := range parts {
			if name, path := attachments.SelectedSkillSource(part.Text); name != "" {
				add(name, path)
				continue
			}
			if frames, ok := attachments.Decode(part.Text); ok {
				for _, frame := range frames {
					name, path, _ := attachments.ParseSkillFrame(frame)
					add(name, path)
				}
				continue
			}
		}
	}
	return skills
}

func instructions(items []map[string]jsonv1.RawMessage, base, workspace string) []guidance.Instruction {
	instructions := []guidance.Instruction{{Text: base, Global: true}}
	for _, item := range items {
		if kind := responses.StringField(item, "type"); kind != "message" && kind != "" {
			continue
		}
		role, text := responses.StringField(item, "role"), messageContentText(item["content"])
		switch {
		case role == "developer" || role == "system":
			instructions = append(instructions, guidance.Instruction{Text: text, Global: true})
		case role == "user" && strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for "):
			if global, repo, ok := strings.Cut(text, "--- project-doc ---"); ok {
				instructions = append(instructions, guidance.Instruction{Text: global, Global: true}, guidance.Instruction{Text: repo})
			} else {
				header, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
				owner := strings.TrimPrefix(header, "# AGENTS.md instructions for ")
				rel, err := filepath.Rel(workspace, owner)
				instructions = append(instructions, guidance.Instruction{Text: text, Global: err != nil || !filepath.IsLocal(rel)})
			}
		}
	}
	return instructions
}

func messageContentText(content jsonv1.RawMessage) string {
	var text string
	if jsonv1.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = jsonv1.Unmarshal(content, &parts)
	var joined strings.Builder
	for _, part := range parts {
		joined.WriteString(part.Text)
		joined.WriteByte('\n')
	}
	return joined.String()
}
