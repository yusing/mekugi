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
type liveDiffCounts = livediff.Counts
type liveDiffRender = livediff.Render
type liveDiffRenderer = livediff.Renderer
type liveDiffOutput = livediff.Output
type liveDiffTheme = livediff.Theme
type liveDiffOSC = livediff.OSC

const (
	liveDiffTerminalTheme  = livediff.TerminalTheme
	liveDiffLightTheme     = livediff.LightTheme
	liveDiffDarkTheme      = livediff.DarkTheme
	maxLiveDiffSyntaxBytes = livediff.MaxSyntaxBytes
)

// RunLiveDiff is the internal entry point for a router-owned terminal pane.
func RunLiveDiff(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace for displayed paths (default current directory)")
	replay := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	simulate := flags.Bool("simulate", false, "replay an isolated streaming UI demonstration; no Codex or Herdr required")
	speed := flags.Float64("speed", 1, "simulation playback speed (0.1 to 20)")
	repeat := flags.Bool("repeat", false, "repeat the simulation until q")
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
	if *simulate {
		if *workspace != "" || *replay != "" || *sessionFile != "" {
			return fail(errors.New("simulation owns its temporary workspace, replay store, and connection"))
		}
		if err := runLiveDiffSimulation(ctx, stdin, stdout, *speed, *repeat); err != nil {
			return fail(err)
		}
		return 0
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
	old, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(stdin.Fd()), old)) }()
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h\x1b]11;?\x1b\\"); err != nil {
		return err
	}
	defer func() {
		_, e := io.WriteString(stdout, "\x1b[?2026l\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l")
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
}
