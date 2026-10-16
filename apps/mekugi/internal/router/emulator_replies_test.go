package router

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"

	vt "github.com/charmbracelet/x/vt"
)

// startEmulatorReplies forwards terminal replies until the returned cleanup joins
// the reader and closes the emulator. Release blocked output writes first.
func startEmulatorReplies(output io.Writer, screen *vt.Emulator) func() {
	var stopping atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, err := screen.Read(buffer)
			if err != nil {
				return
			}
			if stopping.Load() && string(buffer[:n]) == "\x1b[?0n" {
				return
			}
			// A closed PTY must not stop draining the emulator's synchronous reply pipe.
			_, _ = output.Write(buffer[:n])
		}
	}()
	return func() {
		stopping.Store(true)
		// Cancel partial control strings before the status-query shutdown handshake.
		_, _ = screen.Write([]byte("\x18\x1b\\\x1b[5n"))
		<-done
		_ = screen.Close()
	}
}

func TestEmulatorRepliesShutdown(t *testing.T) {
	for _, test := range []struct{ name, prefix string }{
		{"empty", ""}, {"partial CSI", "\x1b["}, {"partial OSC", "\x1b]12;partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				screen := vt.NewEmulator(20, 4)
				// Shutdown must complete even when the terminal no longer accepts replies.
				failedWrite := make(chan struct{}, 1)
				stop := startEmulatorReplies(emulatorClosedOutput{failedWrite}, screen)
				if _, err := screen.Write([]byte("\x1b[5n")); err != nil {
					t.Fatal(err)
				}
				<-failedWrite
				if _, err := screen.Write([]byte(test.prefix)); err != nil {
					t.Fatal(err)
				}
				stop()
				if _, err := screen.Write([]byte("closed")); !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("write after cleanup = %v", err)
				}
			})
		})
	}
}

type emulatorClosedOutput struct{ writes chan<- struct{} }

func (w emulatorClosedOutput) Write([]byte) (int, error) {
	w.writes <- struct{}{}
	return 0, io.ErrClosedPipe
}
