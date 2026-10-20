package terminal

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// WithRawPane owns raw mode, screen setup, and a cancellable key reader for a
// router pane. The reader is joined before terminal state is restored.
func WithRawPane(ctx context.Context, stdin, stdout *os.File, enter, leave string, body func(<-chan byte) error) (err error) {
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
	// Reopening /dev/stdin on macOS shares file-status flags with stdio. Go
	// enables nonblocking mode on the reopened file, which can break stdout.
	// Cancel the read without reopening or closing the caller's descriptor.
	input, err := cancelreader.NewReader(stdin)
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
	defer func() {
		cancel()
		input.Cancel()
		<-done
		err = errors.Join(err, input.Close())
	}()
	return body(keys)
}
