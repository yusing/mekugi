package shellhook

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Invocation is only the host-resolved sessionFlags layer. Copying merged
// configuration would duplicate hooks that Codex discovers in their own layers.
type Invocation struct {
	PreToolUse []jsontext.Value          `json:"PreToolUse"`
	State      map[string]jsontext.Value `json:"state"`
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
func Discover(ctx context.Context, command *exec.Cmd) (prior Invocation, disabled []string, err error) {
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
