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

// Invocation is the caller-owned part of the session flag hook layer.
type Invocation struct {
	PreToolUse []jsontext.Value          `json:"PreToolUse"`
	State      map[string]jsontext.Value `json:"state"`
}

// Discover resolves caller configuration without starting a thread or turn.
func Discover(ctx context.Context, command *exec.Cmd) (configuration jsontext.Value, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cwd, err := filepath.Abs(command.Dir)
	if err != nil {
		return nil, err
	}
	probe := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	probe.Dir, probe.Env, probe.WaitDelay = cwd, command.Environ(), time.Second
	client, err := appserver.Start(probe)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, client.Shutdown())
		if diagnostic := client.Diagnostics.Snapshot(); err != nil && diagnostic != "" {
			err = fmt.Errorf("%w\n%s", err, diagnostic)
		}
	}()
	id, err := client.Initialize()
	if err != nil {
		return nil, err
	}
	if _, err := reply(ctx, client, id); err != nil {
		return nil, err
	}
	if _, err := client.Send("initialized", nil, false); err != nil {
		return nil, err
	}
	id, err = client.Send("config/read", map[string]any{"cwd": cwd, "includeLayers": false}, true)
	if err != nil {
		return nil, err
	}
	data, err := reply(ctx, client, id)
	if err != nil {
		return nil, err
	}
	var config struct {
		Config jsontext.Value `json:"config"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config.Config, nil
}

func reply(ctx context.Context, client *appserver.Client, id string) (jsontext.Value, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case message, ok := <-client.Messages:
			if !ok {
				return nil, errors.New("Codex disconnected during configuration discovery")
			}
			if string(message.ID) != id {
				continue
			}
			if message.Error != nil {
				return nil, fmt.Errorf("read host configuration: %s", message.Error.Message)
			}
			return message.Result, nil
		}
	}
}
