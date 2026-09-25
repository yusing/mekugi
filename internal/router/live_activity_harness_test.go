package router

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"os"
	"os/signal"
	"syscall"
)

// RunLiveActivity is a test-only renderer harness, not a CLI command.
func RunLiveActivity(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-activity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionFile := flags.String("session-file", "", "private router event connection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi live-activity:", err)
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected arguments"))
	}
	if *sessionFile == "" {
		return fail(errors.New("live-activity is a router-owned pane; start an interactive mekugi codex session"))
	}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fail(errors.New("live view needs a terminal"))
	}
	connection, err := readLiveDiffConnection(*sessionFile)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		return fail(err)
	}
	err = withRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006h\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1006l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
		streamCtx, cancelStream := context.WithCancel(ctx)
		events := make(chan activityPaneEvent, 32)
		streamDone := make(chan struct{})
		go func() { defer close(streamDone); liveActivityStream(streamCtx, connection, events) }()
		defer func() { cancelStream(); <-streamDone }()
		resizes := make(chan os.Signal, 1)
		signal.Notify(resizes, syscall.SIGWINCH)
		defer signal.Stop(resizes)
		view := newLiveActivityView()
		return runLiveActivityTerminal(ctx, stdout, view, events, keys, resizes)
	})
	if err != nil {
		return fail(err)
	}
	return 0
}
