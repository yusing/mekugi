package shellhook

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Invocation retains the selected compaction prompt and only the host-resolved
// sessionFlags hook layer. Copying merged hooks would duplicate other layers.
type Invocation struct {
	CompactPrompt string                    `json:"-"`
	PreToolUse    []jsontext.Value          `json:"PreToolUse"`
	State         map[string]jsontext.Value `json:"state"`
}

type hook struct {
	Key     string `json:"key"`
	Event   string `json:"eventName"`
	Handler string `json:"handlerType"`
	Hash    string `json:"currentHash"`
	Matcher string `json:"matcher"`
	Trust   string `json:"trustStatus"`
	Enabled bool   `json:"enabled"`
	Async   bool   `json:"async"`
	Managed bool   `json:"isManaged"`
}

func catalog(data []byte, cwd string) ([]hook, error) {
	var result struct {
		Data []struct {
			Cwd    string           `json:"cwd"`
			Errors []jsontext.Value `json:"errors"`
			Hooks  []hook           `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("read approval hooks: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, errors.New("cannot verify hooks for the selected workspace")
	}
	var hooks []hook
	for _, entry := range result.Data {
		if entry.Cwd != cwd || len(entry.Errors) != 0 {
			return nil, errors.New("cannot verify hooks for the selected workspace")
		}
		hooks = append(hooks, entry.Hooks...)
	}
	return hooks, nil
}

func (h hook) shellConflict() (bool, error) {
	if h.Event != "preToolUse" || !h.Enabled || h.Async || h.Trust != "trusted" && h.Trust != "managed" {
		return false, nil
	}
	matches, err := matchesShell(h.Matcher)
	if err != nil {
		return false, fmt.Errorf("cannot verify hook matcher %q", h.Matcher)
	}
	return matches, nil
}

// Discover starts no thread or turn. The process resolves caller configuration
// and is joined before the caller starts its final, guarded invocation.
func Discover(ctx context.Context, command *exec.Cmd) (Invocation, []string, error) {
	return discover(ctx, command, true)
}
func DiscoverCompact(ctx context.Context, command *exec.Cmd) (Invocation, error) {
	prior, _, err := discover(ctx, command, false)
	return prior, err
}
func discover(ctx context.Context, command *exec.Cmd, guard bool) (prior Invocation, disabled []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cwd, err := filepath.Abs(command.Dir)
	if err != nil {
		return prior, nil, err
	}
	probe := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	probe.Dir, probe.Env, probe.WaitDelay = cwd, command.Environ(), time.Second
	client, err := appserver.Start(probe)
	if err != nil {
		return prior, nil, err
	}
	defer func() { err = errors.Join(err, client.Shutdown()) }()
	id, err := client.Initialize()
	if err != nil {
		return prior, nil, err
	}
	if _, err := reply(ctx, client, id); err != nil {
		return prior, nil, err
	}
	if _, err := client.Send("initialized", nil, false); err != nil {
		return prior, nil, err
	}
	id, err = client.Send("config/read", map[string]any{"cwd": cwd, "includeLayers": true}, true)
	if err != nil {
		return prior, nil, err
	}
	data, err := reply(ctx, client, id)
	if err != nil {
		return prior, nil, err
	}
	var config struct {
		Config struct {
			Prompt string `json:"compact_prompt"`
			File   string `json:"experimental_compact_prompt_file"`
		} `json:"config"`
		Layers *[]struct {
			Name struct {
				Type string `json:"type"`
			} `json:"name"`
			Config struct {
				Hooks Invocation `json:"hooks"`
			} `json:"config"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return prior, nil, err
	}
	if config.Layers == nil {
		return prior, nil, errors.New("Codex did not return requested configuration layers")
	}
	for _, layer := range *config.Layers {
		if layer.Name.Type == "sessionFlags" {
			prior = layer.Config.Hooks
		}
	}
	prior.CompactPrompt = strings.TrimSpace(config.Config.Prompt)
	if prior.CompactPrompt == "" && config.Config.File != "" {
		path := config.Config.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return prior, nil, fmt.Errorf("read caller compaction prompt: %w", err)
		}
		prior.CompactPrompt = strings.TrimSpace(string(data))
	}
	if prior.CompactPrompt == "" {
		prior.CompactPrompt = DefaultCompactPrompt
	}
	if !guard {
		return prior, nil, nil
	}
	id, err = client.Send("hooks/list", map[string]any{"cwds": []string{cwd}}, true)
	if err != nil {
		return prior, nil, err
	}
	data, err = reply(ctx, client, id)
	if err != nil {
		return prior, nil, err
	}
	hooks, err := catalog(data, cwd)
	if err != nil {
		return prior, nil, err
	}
	for _, hook := range hooks {
		matches, err := hook.shellConflict()
		if err != nil {
			return prior, nil, err
		}
		if !matches {
			continue
		}
		if hook.Managed {
			return prior, nil, fmt.Errorf("managed shell hook %q conflicts with the shell guard and cannot be replaced", hook.Key)
		}
		disabled = append(disabled, hook.Key)
	}
	return prior, disabled, nil
}

// Source: codex-rs/prompts/templates/compact/prompt.md:1:10@0df4aa677.
// DefaultCompactPrompt is the stock plaintext handover prompt, pinned for launch.
const DefaultCompactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.
`

func reply(ctx context.Context, client *appserver.Client, id string) (jsontext.Value, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case message, ok := <-client.Messages:
			if !ok {
				return nil, errors.New("Codex disconnected during shell-hook discovery")
			}
			if string(message.ID) != id {
				continue
			}
			if message.Error != nil {
				return nil, fmt.Errorf("discover shell hooks: %s", message.Error.Message)
			}
			return message.Result, nil
		}
	}
}
