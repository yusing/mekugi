package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Claude uses its own native baseline. Only captured custom instructions and
// the selected frontend/coordination contract are appended to that baseline.
func claudeSystemPrompt(settings control.Settings, identity control.Identity, frontendGuidance string) (jsontext.Value, error) {
	var sections []string
	for _, custom := range []*string{settings.BaseInstructions, settings.DeveloperInstructions} {
		if custom != nil && *custom != "" {
			sections = append(sections, *custom)
		}
	}
	project, err := claudeProjectInstructions(settings.Workspace)
	if err != nil {
		return nil, err
	}
	sections = append(sections, project...)
	sections = append(sections, fmt.Sprintf("Mekugi owns coordination for this Claude runtime. Use the threadcontrol MCP tools for agents, messages and waits. Assignments and messages retain their original sender and authority. Your native session identity is %s. Your working directory is %s; agents share the filesystem. Call wait_agent alone to wait for completion; its successful tool batch ends this turn, and an attributed continuation delivers the outcome. Omit timeout_ms unless there is a real deadline. Native tools follow the captured approval policy; filesystem and network sandboxing are disabled. Invocation-private executable frontends are available on PATH; explicit mchanges IDs and ranges read the shared retained namespace, while implicit selections remain caller-scoped.", identity.Thread, settings.Workspace))
	if frontendGuidance != "" {
		sections = append(sections, frontendGuidance)
	}
	return json.Marshal(map[string]string{"type": "preset", "preset": "claude_code", "append": strings.Join(sections, "\n\n")})
}

func claudeProjectInstructions(workspace string) ([]string, error) {
	var directories []string
	for directory := filepath.Clean(workspace); ; directory = filepath.Dir(directory) {
		directories = append(directories, directory)
		if filepath.Dir(directory) == directory {
			break
		}
	}
	var instructions []string
	for _, directory := range slices.Backward(directories) {
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
			path := filepath.Join(directory, name)
			file, err := sourceparse.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("load project instructions: %w", err)
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				file.Close()
				return nil, fmt.Errorf("project instructions must be a readable regular file: %s", path)
			}
			body, readErr := io.ReadAll(file)
			err = errors.Join(readErr, file.Close())
			if err != nil {
				return nil, fmt.Errorf("load project instructions: %w", err)
			}
			instructions = append(instructions, fmt.Sprintf("Project instructions from %s:\n%s", path, body))
			break
		}
	}
	return instructions, nil
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
