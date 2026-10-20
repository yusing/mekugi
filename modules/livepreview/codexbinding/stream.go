package codexbinding

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/modules/livepreview/projection"

	"github.com/yusing/mekugi/libraries/diffmodel"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

type Stream struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	finalCtx  context.Context
	stopFinal context.CancelFunc
	broker    *previewmodel.Session
	preview   previewmodel.Preview
	input     strings.Builder
	wake      chan struct{}
	done      chan struct{}
	closed    bool
	finishing bool
	kind      string
	timing    Timing
	readPatch func(string) (string, bool, error)
}

// Source: apps/mekugi/internal/router/live_diff_preview.go:24:315@72549ff1.
// Identity is the admitted immutable scope of one arriving tool input.
type Identity struct{ Workspace, Thread, Turn, Caller, Kind string }

// Timing is supplied by the renderer without making source projection depend on it.
type Timing struct{ Frame, Unit, Finish time.Duration }

// Start owns one input stream and its first-success baseline cache. The reader
// retains the caller's bounded patch-read contract.
func Start(ctx context.Context, broker *previewmodel.Session, id Identity, timing Timing, readPatch func(string) (string, bool, error)) *Stream {
	ctx, cancel := context.WithCancel(projection.WithSources(ctx, false))
	// Accepted final input can outlive its transport, not its attachment.
	finalCtx, stopFinal := context.WithCancel(context.WithoutCancel(ctx))
	worker := &Stream{
		ctx: ctx, cancel: cancel, broker: broker, kind: id.Kind, timing: timing, readPatch: readPatch,
		finalCtx: finalCtx, stopFinal: stopFinal,
		preview: previewmodel.Preview{ID: rand.Text(), Workspace: id.Workspace, Thread: id.Thread, Turn: id.Turn, Caller: id.Caller},
		wake:    make(chan struct{}, 1), done: make(chan struct{}),
	}
	stop := context.AfterFunc(broker.Context(), worker.Stop)
	go func() { defer stop(); worker.run() }()
	return worker
}

// Prime supplies a captured baseline without replacing an earlier successful read.
func (w *Stream) Prime(path, content string, exists bool) {
	projection.Prime(w.ctx, w.readPatch, path, content, exists)
}

func (w *Stream) Done() <-chan struct{} { return w.done }

func (worker *Stream) Append(delta string) {
	worker.mu.Lock()
	if !worker.closed && !worker.finishing {
		if worker.input.Len()+len(delta) > 256<<10 {
			worker.closed = true
			worker.cancel()
			worker.preview.Files = nil
			worker.preview.Status = previewmodel.PreviewEdit
			worker.broker.Update(worker.preview, false)
		} else {
			worker.input.WriteString(delta)
			select {
			case worker.wake <- struct{}{}:
			default:
			}
		}
	}
	worker.mu.Unlock()
}

// Complete from the authoritative done payload, not whichever delta happened
// to reach the coalesced renderer last. Projection stays off the forwarding path.
func (worker *Stream) Finish(input string) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed || worker.finishing {
		return
	}
	if len(input) > 256<<10 {
		worker.closed = true
		worker.cancel()
		worker.broker.Discard(worker.preview.ID)
		return
	}
	worker.input.Reset()
	worker.input.WriteString(input)
	worker.finishing = true
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

func (worker *Stream) Stop() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed {
		return
	}
	worker.closed = true
	worker.cancel()
	worker.stopFinal()
	worker.broker.Discard(worker.preview.ID)
}

func (w *Stream) run() {
	defer close(w.done)
	defer w.cancel()
	defer w.stopFinal()
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.broker.Discard(w.preview.ID)
		w.mu.Unlock()
	}()
	// Only edits are displayed, revealed by received line. JSON and JavaScript
	// input encode that line break as an escape.
	encoded := w.kind == "exec" || w.kind == nativeExecCommandToolName
	var pacer liveDiffPreviewPacer
	edited := false // A later unprojectable frame keeps the last displayed edit.
	final := false
	revealed := -1
	backlog := false
	var lastFiles []diffmodel.ReviewFile
	var lastReveal, lastAdvance, finishedAt time.Time
	for {
		if final {
			return
		}
		if !backlog {
			select {
			case <-w.ctx.Done():
			case <-w.wake:
			}
		}
		// Coalesce bursts, but keep producing while the provider is still
		// streaming. No filesystem work runs on the provider forwarding path.
		timer := time.NewTimer(w.timing.Frame)
		select {
		case <-w.ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		select {
		case <-w.wake:
		default:
		}
		w.mu.Lock()
		if w.closed || w.broker.Context().Err() != nil || w.ctx.Err() != nil && !w.finishing {
			w.mu.Unlock()
			return
		}
		input := w.input.String()
		preview := w.preview
		final, preview.Complete = w.finishing, w.finishing
		w.mu.Unlock()
		if final && finishedAt.IsZero() {
			finishedAt = time.Now()
		}
		// Do not manufacture a live shell stream after completion if no target
		// diff was shown while input was arriving. The terminal can keep such a
		// completion out of its live dock while retaining the final projection.
		release := final && (encoded && len(lastFiles) == 0 || time.Since(finishedAt) >= w.timing.Finish)
		// Provider deltas arrive in bursts. Reveal the received input at a steady
		// pace instead of jumping per burst. A completed call waits briefly for the
		// reveal to catch up; a cancelled transport shows its final input at once.
		shown := len(input)
		if w.ctx.Err() == nil && !release {
			frames := 1
			if !lastAdvance.IsZero() {
				frames = int(time.Since(lastAdvance) / w.timing.Frame)
			}
			lastAdvance = time.Now()
			shown = pacer.advance(input, frames, final, encoded)
			if pacer.cursor == len(input) && liveDiffLineBoundary(input, shown, len(input), encoded) == shown {
				// Envelope fields (such as workdir) may follow the last source
				// newline. Decode them too; projection gates decoded source lines.
				shown = len(input)
			}
		}
		backlog = pacer.cursor < len(input) || liveDiffLineBoundary(input, shown, len(input), encoded) > shown
		if shown < len(input) {
			input, final, preview.Complete = input[:shown], false, false
		}
		if !final && shown == revealed {
			continue
		}
		revealed = shown
		if final && input == "" {
			w.broker.Update(preview, false)
			continue
		}
		projectionContext := w.ctx
		if final {
			projectionContext = w.finalCtx
		}
		projected, recognized := Project(projectionContext, preview.Workspace, w.kind, input, final, w.readPatch)
		edited = edited || recognized
		if !edited {
			continue
		}
		if !recognized {
			// A retained projection stays until a later frame replaces it.
			projected = previewmodel.Preview{Status: previewmodel.PreviewEdit}
		}
		projected.Complete = final
		projected.ID, projected.Workspace, projected.Thread, projected.Caller = preview.ID, preview.Workspace, preview.Thread, preview.Caller
		projected.Turn = preview.Turn
		changed := len(projected.Files) != 0 && !slices.Equal(projected.Files, lastFiles)
		if changed && !release && !lastReveal.IsZero() && w.ctx.Err() == nil {
			// Transport candidates may arrive every animation frame. Reveal a
			// new target unit only after the preceding one has settled visibly.
			if delay := time.Until(lastReveal.Add(w.timing.Unit)); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-w.ctx.Done():
					timer.Stop()
				}
			}
		}
		w.mu.Lock()
		if final && projected.Status == previewmodel.PreviewUnavailable+"patch cannot be projected" {
			// A speculative patch projection cannot establish failure or success.
			// Clear any earlier preview and leave the result to the host tool.
			w.broker.Update(previewmodel.Preview{
				ID: preview.ID, Workspace: preview.Workspace, Thread: preview.Thread, Turn: preview.Turn, Complete: true,
			}, false)
			w.mu.Unlock()
			continue
		}
		// One preview is in flight, with only the latest input sampled next.
		// Final content and completion share one replaceable snapshot, so a slow
		// viewer cannot receive only removal after losing the last content frame.
		if !w.closed && (w.ctx.Err() == nil || final) && (final || !strings.HasPrefix(projected.Status, previewmodel.PreviewUnavailable)) {
			w.broker.Update(projected, false)
			if changed {
				lastFiles, lastReveal = projected.Files, time.Now()
			}
		}
		w.mu.Unlock()
	}
}
