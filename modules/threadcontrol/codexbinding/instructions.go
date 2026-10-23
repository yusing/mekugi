package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (d *Driver) Instructions(ctx context.Context, thread, requestModel string) ([]guidance.Replacement, error) {
	member, ok := d.control.Member(thread)
	if !ok {
		return nil, errors.New("instruction projection requires an admitted thread")
	}
	owner, err := d.runtime(thread)
	if err != nil {
		return nil, err
	}
	path := member.Path
	if path == "" {
		info, err := owner.Thread(ctx, thread)
		if err != nil {
			return nil, err
		}
		if info.Thread != thread {
			return nil, errors.New("instruction source identity conflicts with admitted thread")
		}
		path = info.Path
	}
	fragments, err := binding.ReadInstructionFragments(thread, path)
	if err != nil {
		return nil, err
	}
	fragments = slices.DeleteFunc(fragments, func(part guidance.Fragment) bool {
		return part.Kind != "multi_agent.role_instructions" && part.Kind != "multi_agent.usage_hint" && part.Kind != "multi_agent.mode_instructions"
	})
	if err := d.control.RetainInstructions(ctx, thread, path, fragments); err != nil {
		return nil, err
	}
	member, _ = d.control.Member(thread)
	if !slices.ContainsFunc(member.Instructions, func(part guidance.Fragment) bool {
		return part.Kind == "multi_agent.role_instructions" || part.Kind == "multi_agent.usage_hint"
	}) {
		return nil, errors.New("required native role or usage instruction provenance is unavailable")
	}
	var baseline string
	if member.Parent != "" {
		baseline = "You are a delegated agent completing the assigned task."
	}
	d.mu.RLock()
	for _, model := range d.models {
		if model.Name != requestModel {
			continue
		}
		selected := model.Root
		if member.Parent != "" {
			selected = model.Child
		}
		if selected != nil {
			baseline = *selected
		}
		break
	}
	d.mu.RUnlock()
	var config map[string]jsontext.Value
	if len(member.Settings.Config) > 0 {
		if err := json.Unmarshal(member.Settings.Config, &config); err != nil {
			return nil, err
		}
		if raw, ok := config["features.multi_agent_v2.root_agent_usage_hint_text"]; ok {
			if err := json.Unmarshal(raw, &baseline); err != nil {
				return nil, err
			}
		}
	}
	text, err := collaborationGuidance(*member, baseline)
	if err != nil {
		return nil, err
	}
	raw, err := owner.Configuration(ctx, thread, member.Settings.Workspace)
	if err != nil {
		return nil, err
	}
	var effective struct {
		Layers []ConfigLayer `json:"layers"`
	}
	if err := json.Unmarshal(raw, &effective); err != nil {
		return nil, err
	}
	roles, err := roleCatalog(effective.Layers)
	if err != nil {
		return nil, err
	}
	text += roleGuidance(roles)
	roleParts := make(map[string]bool)
	for _, part := range member.Instructions {
		if part.Kind == "multi_agent.role_instructions" {
			roleParts[part.Item] = true
		}
	}
	var replacements []guidance.Replacement
	for _, part := range member.Instructions {
		if part.Kind != "multi_agent.role_instructions" && part.Kind != "multi_agent.usage_hint" {
			continue
		}
		value := text
		if part.Kind == "multi_agent.usage_hint" && roleParts[part.Item] {
			value = ""
		}
		replacements = append(replacements, guidance.Replacement{Fragment: part, Text: value})
	}
	return replacements, nil
}

// Feature-owned guidance describes the admitted logical tree and actual tools.
// Model/custom role text comes from the selected invocation, never bundled copies.
func collaborationGuidance(member control.Member, baseline string) (string, error) {
	if strings.Contains(baseline, "functions.collaboration.") || strings.Contains(baseline, "mekugi_collaboration.") {
		return "", errors.New("configured agent guidance uses unavailable native collaboration tools")
	}
	identity := "You are the root agent."
	if member.Parent != "" {
		parent, _, _ := strings.CutLast(member.Task, "/")
		identity = fmt.Sprintf("You are a child agent. Your parent logical task is %s.", parent)
	}
	baseline += "\n\n" + identity
	return fmt.Sprintf("%s\n\nYour logical task is %s. Use tools.mcp__threadcontrol__spawn_agent, tools.mcp__threadcontrol__followup_task, tools.mcp__threadcontrol__send_message, tools.mcp__threadcontrol__interrupt_agent, tools.mcp__threadcontrol__list_agents and tools.mcp__threadcontrol__wait_agent inside functions.exec.\nAgents share the filesystem; this thread's working directory is %q. There are %d concurrency slots, including the root. Override model or reasoning effort only when explicitly requested by the user, applicable AGENTS.md instructions, or skill instructions. Such overrides require fork_turns=none; full-history forks inherit both. Messages identify their sender and recipient. send_message does not start idle work; followup_task does. wait_agent registers selected descendant events without polling.", baseline, member.Task, member.Settings.Workspace, member.Limits.Concurrency), nil
}

func roleGuidance(roles map[string]roleDefinition) string {
	var out strings.Builder
	out.WriteString("\nAvailable agent_type roles:")
	for _, name := range slices.Sorted(maps.Keys(roles)) {
		role := roles[name]
		if role.err != nil {
			continue
		}
		fmt.Fprintf(&out, "\n- %s: %s", name, role.selection.Description)
		for _, field := range []struct {
			name  string
			value *string
		}{{"model", role.values.Model}, {"model_reasoning_effort", role.values.Effort}} {
			if field.value != nil {
				fmt.Fprintf(&out, " (%s: %s, role-selected)", field.name, *field.value)
			}
		}
	}
	return out.String()
}
