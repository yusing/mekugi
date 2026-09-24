package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
)

const maxHerdrAPIResponseBytes = 1 << 20

type liveDiffPane struct {
	id          string
	sessionFile string
	executable  string
}

type herdrPaneIdentity struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
}

// A tab this wide (a 27-inch or larger display) gives each Mekugi pane its own
// column; narrower tabs, such as laptops, stack them in one side column.
const mekugiWideTabColumns = 240

type mekugiPaneKind int

const (
	mekugiDiffPane mekugiPaneKind = iota
	mekugiAgentsPane
	mekugiRosterPane
)

// mekugiPlacement is where a new pane goes. Ratio is the share of the target
// pane's space that the target keeps; zero leaves Herdr's default.
type mekugiPlacement struct {
	target, split string
	ratio         float64
}

// placeMekugiPane chooses a responsive placement. The agents roster sits under
// the caller, which keeps most of its height. Of the diff and agents activity
// panes, the first opens beside the caller. The second joins the first as a
// column on wide tabs, or below it otherwise, keeping a little more room for
// the live diff in either case.
func placeMekugiPane(caller, neighbor string, kind mekugiPaneKind, tabWidth int) mekugiPlacement {
	agents := kind == mekugiAgentsPane
	switch {
	case kind == mekugiRosterPane:
		return mekugiPlacement{target: caller, split: "down", ratio: 0.8}
	case neighbor == "":
		return mekugiPlacement{target: caller, split: "right"}
	case tabWidth >= mekugiWideTabColumns && agents:
		return mekugiPlacement{target: neighbor, split: "right", ratio: 0.55}
	case tabWidth >= mekugiWideTabColumns:
		return mekugiPlacement{target: neighbor, split: "right", ratio: 0.45}
	case agents:
		return mekugiPlacement{target: neighbor, split: "down", ratio: 0.55}
	}
	return mekugiPlacement{target: neighbor, split: "down", ratio: 0.45}
}

func splitLiveDiff(ctx context.Context, workspace, replay, below string, lifetime *liveDiffPane) error {
	if lifetime == nil || lifetime.sessionFile == "" {
		return errors.New("live diff requires a router session")
	}
	return splitMekugiPane(ctx, workspace, "Mekugi live diff", below, mekugiDiffPane, lifetime, func(executable string) []string {
		return []string{executable, "live-diff", "--workspace", workspace, "--replay-dir", replay, "--session-file", lifetime.sessionFile}
	})
}

// splitLiveActivity places the agents pane beside or below another Mekugi pane
// when one exists, otherwise beside its caller.
func splitLiveActivity(ctx context.Context, workspace, below string, lifetime *liveDiffPane) error {
	if lifetime == nil || lifetime.sessionFile == "" {
		return errors.New("live activity requires a router session")
	}
	return splitMekugiPane(ctx, workspace, "Mekugi agents", below, mekugiAgentsPane, lifetime, func(executable string) []string {
		return []string{executable, "live-activity", "--session-file", lifetime.sessionFile}
	})
}

// splitLiveRoster places the agents roster under its caller. It observes the
// agents pane's stream, so it shares that pane's session file.
func splitLiveRoster(ctx context.Context, workspace string, lifetime *liveDiffPane) error {
	if lifetime == nil || lifetime.sessionFile == "" {
		return errors.New("live roster requires a router session")
	}
	return splitMekugiPane(ctx, workspace, "Mekugi roster", "", mekugiRosterPane, lifetime, func(executable string) []string {
		return []string{executable, "live-activity", "--view", "roster", "--session-file", lifetime.sessionFile}
	})
}

func splitMekugiPane(ctx context.Context, workspace, label, neighbor string, kind mekugiPaneKind, lifetime *liveDiffPane, command func(string) []string) error {
	if os.Getenv("HERDR_ENV") != "1" {
		return errors.New("live diff requires a Herdr-managed pane")
	}
	callerPane := os.Getenv("HERDR_PANE_ID")
	if callerPane == "" {
		return errors.New("live diff requires a Herdr caller pane")
	}
	var current struct {
		Type string            `json:"type"`
		Pane herdrPaneIdentity `json:"pane"`
	}
	if err := callHerdrAPI(ctx, "mekugi:live-diff:current", "pane.current", map[string]any{
		"caller_pane_id": callerPane,
	}, &current); err != nil {
		return err
	}
	if current.Type != "pane_current" || current.Pane.PaneID == "" ||
		current.Pane.TabID == "" || current.Pane.WorkspaceID == "" {
		return errors.New("herdr current-pane lookup returned no pane identity")
	}

	executable := lifetime.executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	var created struct {
		Type   string `json:"type"`
		Layout struct {
			TabID         string `json:"tab_id"`
			FocusedPaneID string `json:"focused_pane_id"`
		} `json:"layout"`
	}
	if err := callHerdrAPI(ctx, "mekugi:live-diff:create", "layout.apply", map[string]any{
		"workspace_id": current.Pane.WorkspaceID,
		"tab_label":    label,
		"focus":        false,
		"root": map[string]any{
			"type":    "pane",
			"label":   label,
			"cwd":     workspace,
			"command": command(executable),
		},
	}, &created); err != nil {
		return err
	}
	if created.Type != "layout_apply" || created.Layout.TabID == "" || created.Layout.FocusedPaneID == "" {
		return errors.New("herdr direct launch returned no pane identity")
	}
	lifetime.id = created.Layout.FocusedPaneID

	// Tab geometry only refines placement; without it the panes stack.
	tabWidth := 0
	if neighbor != "" {
		var layout struct {
			Layout struct {
				Area struct {
					Width int `json:"width"`
				} `json:"area"`
			} `json:"layout"`
		}
		if callHerdrAPI(ctx, "mekugi:live-diff:layout", "pane.layout", map[string]any{"pane_id": current.Pane.PaneID}, &layout) == nil {
			tabWidth = layout.Layout.Area.Width
		}
	}
	placement := placeMekugiPane(current.Pane.PaneID, neighbor, kind, tabWidth)
	destination := map[string]any{
		"type":           "tab",
		"tab_id":         current.Pane.TabID,
		"target_pane_id": placement.target,
		"split":          placement.split,
	}
	if placement.ratio > 0 {
		destination["ratio"] = placement.ratio
	}
	var moved struct {
		Type       string `json:"type"`
		MoveResult struct {
			Changed     bool              `json:"changed"`
			ClosedTabID string            `json:"closed_tab_id"`
			Pane        herdrPaneIdentity `json:"pane"`
		} `json:"move_result"`
	}
	if err := callHerdrAPI(ctx, "mekugi:live-diff:move", "pane.move", map[string]any{
		"pane_id":     lifetime.id,
		"focus":       false,
		"destination": destination,
	}, &moved); err != nil {
		return fmt.Errorf("created direct pane %s but could not place it: %w", lifetime.id, err)
	}
	if moved.Type != "pane_move" || !moved.MoveResult.Changed ||
		moved.MoveResult.Pane.PaneID == "" || moved.MoveResult.ClosedTabID != created.Layout.TabID ||
		moved.MoveResult.Pane.TabID != current.Pane.TabID ||
		moved.MoveResult.Pane.WorkspaceID != current.Pane.WorkspaceID {
		return fmt.Errorf("herdr did not place direct pane %s beside its caller", lifetime.id)
	}
	lifetime.id = moved.MoveResult.Pane.PaneID
	return nil
}

func callHerdrAPI(ctx context.Context, id, method string, params, result any) error {
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	if socketPath == "" {
		return errors.New("live diff requires a Herdr API socket")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("herdr %s: %w", method, err)
		}
	}
	request := struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxHerdrAPIResponseBytes+1))
	data, readErr := reader.ReadBytes('\n')
	if len(data) > maxHerdrAPIResponseBytes {
		return fmt.Errorf("herdr %s response exceeds 1 MiB", method)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("herdr %s: %w", method, readErr)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("herdr %s returned an empty response", method)
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("herdr %s returned invalid JSON: %w", method, err)
	}
	if response.ID != id {
		return fmt.Errorf("herdr %s returned a mismatched response", method)
	}
	if response.Error != nil {
		return fmt.Errorf("herdr %s: %s: %s", method, response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 {
		return fmt.Errorf("herdr %s returned no result", method)
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("herdr %s returned an invalid result: %w", method, err)
	}
	return nil
}
