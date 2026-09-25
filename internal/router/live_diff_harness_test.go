package router

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Legacy transport harness keeps the existing PTY rendering regressions usable.
// RunLiveDiff is a test-only renderer harness, not a CLI command.
func RunLiveDiff(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace for displayed paths (default current directory)")
	replay := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	sessionFile := flags.String("session-file", "", "private router event connection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi live-diff:", err)
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected arguments"))
	}
	var err error
	if *workspace == "" {
		*workspace, err = os.Getwd()
	}
	if err == nil {
		*workspace, err = filepath.Abs(*workspace)
	}
	if err == nil {
		*workspace, err = canonicalInspectionWorkspace(*workspace)
	}
	if err != nil {
		return fail(err)
	}
	if *replay == "" {
		*replay, err = defaultMekugiReplayDirectory()
	} else {
		*replay, err = filepath.Abs(*replay)
	}
	if err != nil {
		return fail(err)
	}
	if *sessionFile == "" {
		return fail(errors.New("live-diff is a router-owned pane; start an interactive mekugi codex session"))
	}
	store := &mekugiReplayStore{directory: *replay}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fail(errors.New("live view needs a terminal"))
	}
	if err := runLiveDiffTerminal(ctx, store, *workspace, stdin, stdout, *sessionFile); err != nil {
		return fail(err)
	}
	return 0
}

func runLiveDiffTerminal(ctx context.Context, store *mekugiReplayStore, workspace string, stdin, stdout *os.File, sessionFile string) (err error) {
	connection, err := readLiveDiffConnection(sessionFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return withRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
		streamCtx, cancelStream := context.WithCancel(ctx)
		events := make(chan liveDiffEvent, 32)
		streamDone := make(chan struct{})
		go func() { defer close(streamDone); liveDiffStream(streamCtx, connection, events) }()
		defer func() { cancelStream(); <-streamDone }()
		controller := newLiveDiffTerminalController(store, workspace, stdout)
		defer controller.close()
		resizes := make(chan os.Signal, 1)
		signal.Notify(resizes, syscall.SIGWINCH)
		defer signal.Stop(resizes)
		return controller.run(ctx, events, keys, resizes)
	})
}

func (c *liveDiffTerminalController) run(
	ctx context.Context,
	events <-chan liveDiffEvent,
	keys <-chan byte,
	resizes <-chan os.Signal,
) error {
	for {
		if err := c.renderFrame(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.previewFrameC:
			c.previewFrameC, c.dirty = nil, true
			c.previewFrameDue = time.Time{}
		case event, open := <-events:
			if !open {
				return nil
			}
		drainEvents:
			for drained := 0; ; drained++ {
				done, err := c.applyEvent(ctx, event)
				if err != nil || done {
					return err
				}
				// Consume already queued snapshots before painting. Preview updates
				// replace each other; durable events retain their original order.
				if drained >= cap(events) {
					break drainEvents
				}
				select {
				case event, open = <-events:
					if !open {
						return nil
					}
				default:
					break drainEvents
				}
			}
		case <-c.escapeC:
			c.escapeC = nil
			if c.escape == "\x1b" {
				c.escape = ""
				c.navigation.filtering, c.navigation.focused, c.help = false, false, false
				c.dirty = true
			}
		case <-resizes:
			c.dirty = true
		case key, open := <-keys:
			if !open || c.handleKey(key) {
				return nil
			}
		}
	}
}
