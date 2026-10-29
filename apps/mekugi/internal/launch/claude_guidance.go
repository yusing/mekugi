package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Claude uses its own native baseline. Only captured custom instructions and
// the coordination contract are appended to that baseline.
func claudeSystemPrompt(settings control.Settings, identity control.Identity) (jsontext.Value, error) {
	roles, err := threadbinding.RoleCatalog(settings)
	if err != nil {
		return nil, err
	}
	var sections []string
	for _, custom := range []*string{settings.BaseInstructions, settings.DeveloperInstructions} {
		if custom != nil && *custom != "" {
			sections = append(sections, *custom)
		}
	}
	sections = append(sections, fmt.Sprintf("Mekugi owns coordination for this Claude runtime. Use the threadcontrol MCP tools for agents, messages and waits. For a Codex child, use fork_turns=none and a Codex model or a role with a pinned Codex model; list_models returns the selected Codex catalog. Assignments and messages retain their original sender and authority. Your native session identity is %s. Your working directory is %s; agents share the filesystem. Call wait_agent alone to wait for completion; its successful tool batch ends this turn, and an attributed continuation delivers the outcome. Omit timeout_ms unless there is a real deadline. Native tools follow the captured approval policy; filesystem and network sandboxing are disabled. Invocation-private executable frontends are available on PATH; explicit mchanges IDs and ranges read the shared retained namespace, while implicit selections remain caller-scoped.", identity.Thread, settings.Workspace), roles)
	return json.Marshal(map[string]string{"type": "preset", "preset": "claude_code", "append": strings.Join(sections, "\n\n")})
}

// claudeGuidanceSettings delivers frontend guidance as SessionStart context,
// which joins the conversation where Claude Code places project instructions
// instead of the system-prompt tail. A settings command hook runs for startup,
// resume and compaction; SDK callback hooks register too late for startup.
func claudeGuidanceSettings(guidance string) (jsontext.Value, error) {
	if guidance == "" {
		return jsontext.Value("{}"), nil
	}
	output, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "SessionStart", "additionalContext": guidance}})
	if err != nil {
		return nil, err
	}
	command := "printf %s '" + strings.ReplaceAll(string(output), "'", `'\''`) + "'"
	return json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]string{"type": "command", "command": command}}}}}})
}

func claudePreload(ctx context.Context, selection, workspace string, environment []string, changes compact.ChangeReader) (string, error) {
	if selection == "" {
		return "", nil
	}
	snapshot, err := compact.ReadSources(ctx, selection, workspace, environment, changes)
	if err != nil {
		return "", err
	}
	sources, err := compact.Sources(selection)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	for i, source := range sources {
		if source.HostLoaded() {
			continue
		}
		label := source.Path
		if source.Changes != nil {
			label = "mchanges:" + strings.Join(source.Changes, " ")
		}
		if label == "" {
			label = "skill:" + source.Skill
		}
		result := snapshot.Sources[i]
		fmt.Fprintf(&body, "\n<startup-source name=%q exit_code=%d>\n%s", label, result.ExitCode, result.Stdout)
		if result.Stderr != "" {
			fmt.Fprintf(&body, "\nSource diagnostic: %s", result.Stderr)
		}
		body.WriteString("\n</startup-source>\n")
	}
	return body.String(), nil
}
