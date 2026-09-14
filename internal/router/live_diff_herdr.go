package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type liveDiffPane struct {
	id          string
	sessionFile string
}

func splitLiveDiff(ctx context.Context, workspace, replay string, lifetime *liveDiffPane) error {
	if lifetime == nil || lifetime.sessionFile == "" {
		return errors.New("live diff requires a router session")
	}
	if os.Getenv("HERDR_ENV") != "1" {
		return errors.New("live diff requires a Herdr-managed pane")
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "herdr", args...)
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("herdr: %w: %s", err, out)
		}
		return out, nil
	}
	var pane struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			}
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := run("pane", "split", "--current", "--direction", "right", "--cwd", workspace, "--no-focus")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &pane); err != nil || pane.Result.Pane.PaneID == "" {
		return errors.New("Herdr split returned no pane identity")
	}
	id := pane.Result.Pane.PaneID
	command := shellQuoteArgument(executable) + " live-diff --workspace " + shellQuoteArgument(workspace) + " --replay-dir " + shellQuoteArgument(replay)
	lifetime.id = id
	command = "exec " + command + " --session-file " + shellQuoteArgument(lifetime.sessionFile)
	if _, err := run("pane", "run", id, command); err != nil {
		return fmt.Errorf("created pane %s but could not start viewer: %w", id, err)
	}
	return nil
}
