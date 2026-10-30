package launch

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/yusing/mekugi/apps/mekugi/internal/headless"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// runHeadless borrows the ordinary server runtime without a renderer or a
// detached daemon. The task owns admission, evidence delivery and family cleanup.
func runHeadless(ctx context.Context, routerArgs, args []string, input io.Reader, output io.Writer) (int, error) {
	if err := validateCodexArgs(args); err != nil {
		return 2, err
	}
	_, resume, yolo, err := appServerArgs(expandReasoningShortcuts(args))
	if err != nil {
		return 2, err
	}
	if !yolo {
		return 2, errors.New("headless cannot answer approvals; it requires explicit --yolo")
	}
	if resume != "" {
		return 2, errors.New("headless runs a new thread; resume is not supported")
	}
	prompt, err := headless.ReadPrompt(ctx, input)
	if err != nil {
		return 1, err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := headless.NewStream(output, cancel)
	launcher := &serverLauncher{threads: control.New(), headless: stream}
	hub := sessionapi.NewHub(ctx, launcher)
	launcher.hub = hub
	thread, err := hub.RunNative(ctx, sessionapi.NativeStart{Workspace: workspace, RouterArgs: routerArgs, Args: args}, prompt)
	hub.Close()
	err = errors.Join(err, launcher.workers.Close(), stream.Err())
	if err != nil {
		return 1, err
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	if err := stream.Complete(thread); err != nil {
		return 1, err
	}
	return 0, nil
}
