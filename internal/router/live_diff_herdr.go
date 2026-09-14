package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type liveDiffPane struct {
	id          string
	sessionFile string
}

func splitLiveDiff(ctx context.Context, workspace, replay string, stdout io.Writer, lifetime *liveDiffPane) error {
	if os.Getenv("HERDR_ENV") != "1" {
		return errors.New("--herdr requires a Herdr-managed pane")
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
	out, err := run("pane", "layout", "--current")
	if err != nil {
		return err
	}
	var layout struct {
		Result struct {
			Layout struct {
				Panes []struct {
					PaneID string `json:"pane_id"`
					Rect   struct{ Width, Height int }
				}
			}
		}
	}
	if err := json.Unmarshal(out, &layout); err != nil {
		return err
	}
	// Query the caller, never the UI-focused pane.
	current, err := run("pane", "current", "--current")
	if err != nil {
		return err
	}
	var pane struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			}
		}
	}
	if err := json.Unmarshal(current, &pane); err != nil || pane.Result.Pane.PaneID == "" {
		return errors.New("Herdr did not return the caller pane identity")
	}
	direction := ""
	for _, p := range layout.Result.Layout.Panes {
		if p.PaneID == pane.Result.Pane.PaneID {
			direction = "down"
			if p.Rect.Width >= p.Rect.Height*3 {
				direction = "right"
			}
		}
	}
	if direction == "" {
		return errors.New("caller pane is absent from Herdr layout")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	out, err = run("pane", "split", "--current", "--direction", direction, "--cwd", workspace, "--no-focus")
	if err != nil {
		return err
	}
	pane.Result.Pane.PaneID = ""
	if err := json.Unmarshal(out, &pane); err != nil || pane.Result.Pane.PaneID == "" {
		return errors.New("Herdr split returned no pane identity")
	}
	id := pane.Result.Pane.PaneID
	command := shellQuoteArgument(executable) + " live-diff --workspace " + shellQuoteArgument(workspace) + " --replay-dir " + shellQuoteArgument(replay)
	if lifetime != nil {
		lifetime.id = id
		command = "exec " + command + " --session-file " + shellQuoteArgument(lifetime.sessionFile)
	}
	if _, err := run("pane", "run", id, command); err != nil {
		return fmt.Errorf("created pane %s but could not start viewer: %w", id, err)
	}
	_, err = fmt.Fprintf(stdout, "Live diff started in Herdr pane %s (focus unchanged).\n", id)
	return err
}
