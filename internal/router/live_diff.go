package router

import (
	"context"
	"errors"
	"io"
	"os"

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
