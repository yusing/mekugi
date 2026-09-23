package router

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yusing/mekugi/internal/livediff"
	"golang.org/x/term"
)

type liveDiffFile = livediff.File
type liveDiffChunk = livediff.Chunk
type liveDiffView = livediff.View
type liveDiffRender = livediff.Render
type liveDiffRenderer = livediff.Renderer
type liveDiffTheme = livediff.Theme
type liveDiffOSC = livediff.OSC

// RunLiveDiff is the internal entry point for a router-owned terminal pane.
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

// withRawPane owns raw mode, screen setup, and a cancellable key reader for a
// router pane. The reader is joined before terminal state is restored.
func withRawPane(ctx context.Context, stdin, stdout *os.File, enter, leave string, body func(<-chan byte) error) (err error) {
	old, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(stdin.Fd()), old)) }()
	if _, err := io.WriteString(stdout, enter); err != nil {
		return err
	}
	defer func() {
		_, e := io.WriteString(stdout, leave)
		err = errors.Join(err, e)
	}()
	// A private descriptor makes cancellation interrupt Read without closing the
	// caller's stdin. The goroutine is joined before restoring terminal state.
	input, err := os.Open(stdin.Name())
	if err != nil {
		return err
	}
	keys := make(chan byte, 64)
	done := make(chan struct{})
	inputCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		defer close(keys)
		var buf [32]byte
		for {
			n, e := input.Read(buf[:])
			for _, b := range buf[:n] {
				select {
				case keys <- b:
				case <-inputCtx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	defer func() { cancel(); input.Close(); <-done }()
	return body(keys)
}
