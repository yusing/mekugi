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
// the selected frontend/coordination contract are appended to that baseline.
func claudeSystemPrompt(settings control.Settings, identity control.Identity, frontendGuidance string) (jsontext.Value, error) {
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
	if frontendGuidance != "" {
		sections = append(sections, frontendGuidance)
	}
	return json.Marshal(map[string]string{"type": "preset", "preset": "claude_code", "append": strings.Join(sections, "\n\n")})
}

func claudePreload(ctx context.Context, selection, workspace string, environment []string) (string, error) {
	if selection == "" {
		return "", nil
	}
	snapshot, err := compact.ReadSources(ctx, selection, workspace, environment)
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
