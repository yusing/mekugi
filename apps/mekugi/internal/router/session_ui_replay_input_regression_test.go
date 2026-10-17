package router

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestSessionUIReplayQueuedContinuationSurvivesExpiredEscape(t *testing.T) {
	for _, dialog := range []bool{false, true} {
		name := "transcript"
		if dialog {
			name = "dialog"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Both select cases are ready after the stalled frame. Repeat to
				// exercise either arbitration order with controlled time and input.
				for range 16 {
					p := replayPlaybackTestNew(t)
					ctx, cancel := context.WithCancel(t.Context())
					p.ctx = ctx
					if dialog {
						openReplayBlocks(p, p.ui.view, []activityui.Block{{Kind: "text", Body: "first\nsecond\n"}})
					}
					original := p.ui.shell.Dialog
					keys := make(chan byte, 64)
					painting, resume := make(chan struct{}), make(chan struct{})
					frames := 0
					paint := func() error {
						if err := p.paint(io.Discard, 120, 28); err != nil {
							return err
						}
						frames++
						switch frames {
						case 1:
							keys <- 27
						case 2:
							close(painting)
							<-resume
						}
						return nil
					}
					done := make(chan error, 1)
					go func() { done <- p.run(keys, paint, false, io.Discard) }()
					// The first tick follows consumption of the lone ESC.
					select {
					case <-painting:
					case err := <-done:
						cancel()
						t.Fatalf("replay stopped before the stalled frame: %v", err)
					}
					time.Sleep(40 * time.Millisecond) // Fake time, past its deadline.
					for _, key := range []byte("[<64;10;5M+") {
						keys <- key
					}
					close(resume)
					synctest.Wait()
					select {
					case err := <-done:
						cancel()
						t.Fatalf("queued mouse continuation became standalone Escape: %v", err)
					default:
					}
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation: %v", err)
					}
					if p.Speed != 2 || p.ui.shell.Dialog != original {
						t.Fatalf("continuation lost its target or following control: speed=%v dialog=%v", p.Speed, p.ui.shell.Dialog != nil)
					}
				}
			})
		})
	}
}

func TestSessionUIReplayStandaloneEscapeClosesDialogThenQuits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := replayPlaybackTestNew(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		p.ctx = ctx
		openReplayBlocks(p, p.ui.view, []activityui.Block{{Kind: "text", Body: "first\nsecond\n"}})
		keys := make(chan byte, 1)
		done := make(chan error, 1)
		go func() {
			done <- p.run(keys, func() error { return p.paint(io.Discard, 120, 28) }, false, io.Discard)
		}()
		keys <- 27
		synctest.Wait()
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("dialog Escape quit replay: %v", err)
		default:
		}
		if p.ui.shell.Dialog != nil {
			t.Fatal("standalone Escape did not dismiss the dialog")
		}
		keys <- 27
		synctest.Wait()
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("standalone Escape failed: %v", err)
			}
		default:
			t.Fatal("standalone Escape did not quit replay")
		}
	})
}
