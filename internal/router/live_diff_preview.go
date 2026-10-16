package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yusing/mekugi/modules/livepreview/projection"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

type liveDiffPreviewWorker struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	broker    *previewmodel.Session
	preview   previewmodel.Preview
	input     strings.Builder
	wake      chan struct{}
	done      chan struct{}
	closed    bool
	finishing bool
	kind      string
}

func startLiveDiffPreview(ctx context.Context, broker *previewmodel.Session, workspace, thread string, kind ...string) *liveDiffPreviewWorker {
	ctx, cancel := context.WithCancel(projection.WithSources(ctx, false))
	worker := &liveDiffPreviewWorker{
		ctx: ctx, cancel: cancel, broker: broker,
		preview: previewmodel.Preview{ID: rand.Text(), Workspace: workspace, Thread: thread},
		wake:    make(chan struct{}, 1), done: make(chan struct{}),
	}
	if len(kind) != 0 {
		worker.kind = kind[0]
	}
	stop := context.AfterFunc(broker.Context(), cancel)
	go func() { defer stop(); worker.run() }()
	return worker
}

// operationCaller is the canonical agent path of this request's tool calls.
// Older clients may omit a child's name, leaving it unknown rather than main.
func (t *mekugiResponseTransform) operationCaller() string {
	if !t.subagentTurn {
		return "/root"
	}
	return t.commentaryAuthor
}

func (t *mekugiResponseTransform) previewStockDelta(itemID, kind, delta string) {
	if delta == "" {
		return
	}
	auto := t.proxy.review.Preview()
	if auto == nil || !auto.Enabled() {
		return
	}
	if t.previews == nil {
		t.previews = make(map[string]*liveDiffPreviewWorker)
	}
	worker := t.previews[itemID]
	if worker == nil {
		worker = startLiveDiffPreview(t.ctx, auto, t.directory, t.threadID, kind)
		worker.mu.Lock()
		worker.preview.Caller = t.operationCaller()
		worker.preview.Turn = t.shellTurnID
		worker.mu.Unlock()
		t.previews[itemID] = worker
	}
	worker.appendDelta(delta)
}

func (worker *liveDiffPreviewWorker) appendDelta(delta string) {
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

func (t *mekugiResponseTransform) endPreview(itemID string) {
	if worker := t.previews[itemID]; worker != nil {
		worker.stop()
		delete(t.previews, itemID)
	}
}

func (t *mekugiResponseTransform) finishPreview(itemID, input string) {
	if worker := t.previews[itemID]; worker != nil {
		worker.finish(input)
	}
}

// Complete from the authoritative done payload, not whichever delta happened
// to reach the coalesced renderer last. Projection stays off the forwarding path.
func (worker *liveDiffPreviewWorker) finish(input string) {
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

func (worker *liveDiffPreviewWorker) stop() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed {
		return
	}
	worker.closed = true
	worker.cancel()
	worker.broker.Discard(worker.preview.ID)
}

func (w *liveDiffPreviewWorker) projectStockPreview(input, workspace string, final bool) (previewmodel.Preview, bool) {
	preview, ok := nativePatchPreview(input)
	if !ok {
		return previewmodel.Preview{}, false
	}
	projectionContext := w.ctx
	if final {
		projectionContext = context.WithoutCancel(projectionContext)
	}
	ctx, cancel := context.WithTimeout(projectionContext, time.Second)
	defer cancel()
	if !final {
		// Decode first: an outer JavaScript newline need not terminate a
		// patch line inside a string literal.
		tip := strings.LastIndexByte(input, '\n') + 1
		if strings.HasSuffix(input, ";") && strings.HasPrefix(input[tip:], "+") {
			preview.Input = input
		} else {
			preview.Input = input[:tip]
		}
		if preview.Input == "" {
			preview.Input = "\n"
			return preview, true
		}
	}
	preview.Complete = final
	preview.Tool = applyPatchToolName
	projected := projectStockPatchPreview(ctx, workspace, preview)
	return projected, true
}

func (w *liveDiffPreviewWorker) run() {
	defer close(w.done)
	defer w.cancel()
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
		timer := time.NewTimer(diffview.PreviewFrameDelay)
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
		if w.closed || w.ctx.Err() != nil && !w.finishing {
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
		release := final && (encoded && len(lastFiles) == 0 || time.Since(finishedAt) >= diffview.PreviewFinishDrain)
		// Provider deltas arrive in bursts. Reveal the received input at a steady
		// pace instead of jumping per burst. A completed call waits briefly for the
		// reveal to catch up; a cancelled transport shows its final input at once.
		shown := len(input)
		if w.ctx.Err() == nil && !release {
			frames := 1
			if !lastAdvance.IsZero() {
				frames = int(time.Since(lastAdvance) / diffview.PreviewFrameDelay)
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
		projected, recognized := w.project(input, preview.Workspace, final)
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
			if delay := time.Until(lastReveal.Add(diffview.PreviewUnitDelay)); delay > 0 {
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

// project recognizes the edit in a call's received input. Command and script
// text that is not an edit has no preview.
func (w *liveDiffPreviewWorker) project(input, workspace string, final bool) (previewmodel.Preview, bool) {
	shell := func(call codeModeShellCall) (previewmodel.Preview, bool) {
		files, tool, recognized, err := projection.Shell(w.ctx, call.cmd, liveDiffWorkdir(workspace, call.workdir), final)
		switch {
		case recognized && call.dynamicWorkdir:
			return previewmodel.Preview{Status: previewmodel.PreviewUnavailable + "edit target depends on a computed workdir"}, true
		case !recognized || err != nil:
			return previewmodel.Preview{}, recognized
		}
		return previewmodel.Preview{Files: files, Tool: tool, Status: previewmodel.PreviewEdit}, true
	}
	switch w.kind {
	case applyPatchToolName:
		return w.projectStockPreview(input, workspace, final)
	case nativeExecCommandToolName:
		call, ok := liveDiffExecArguments(input)
		if !ok {
			return previewmodel.Preview{}, false
		}
		return shell(call)
	case "exec":
		if calls, ok := binding.ExecutionCalls(input, false); ok {
			for _, call := range slices.Backward(calls) {
				switch responses.StringField(call, "name") {
				case applyPatchToolName:
					return w.projectStockPreview(responses.StringField(call, "input"), workspace, final)
				case nativeExecCommandToolName:
					var arguments map[string]json.RawMessage
					if json.Unmarshal([]byte(responses.StringField(call, "arguments")), &arguments) != nil {
						return previewmodel.Preview{}, false
					}
					return shell(codeModeShellCall{cmd: responses.StringField(arguments, "cmd"), workdir: responses.StringField(arguments, "workdir")})
				}
			}
			return previewmodel.Preview{}, false
		}
		patches := stockLiteralPatchInputs(input)
		if fragment := stockPatchFragment(input); fragment != "" {
			patches = append(patches, fragment)
		}
		if len(patches) != 0 {
			if projected, ok := w.projectStockPreview(patches[len(patches)-1], workspace, final); ok {
				return projected, true
			}
		}
		calls := codeModeShellFragments(input)
		if len(calls) == 0 {
			return previewmodel.Preview{}, false
		}
		return shell(calls[len(calls)-1])
	default:
		program := input
		if programs, err := shellsyntax.Split(input); err == nil {
			// Preview the current program, not earlier shell framing.
			program = programs[len(programs)-1]
		}
		return shell(codeModeShellCall{cmd: program})
	}
}

// liveDiffExecArguments reads exec_command arguments, including an unfinished
// JSON object whose cmd string is still arriving.
func liveDiffExecArguments(input string) (codeModeShellCall, bool) {
	var arguments map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &arguments) == nil {
		call := codeModeShellCall{cmd: responses.StringField(arguments, "cmd"), workdir: responses.StringField(arguments, "workdir")}
		if raw, exists := arguments["workdir"]; exists && json.Unmarshal(raw, new(string)) != nil {
			call.dynamicWorkdir = true
		}
		return call, call.cmd != ""
	}
	start := strings.IndexFunc(input, func(char rune) bool { return !unicode.IsSpace(char) })
	if start < 0 || input[start] != '{' {
		return codeModeShellCall{}, false
	}
	call, _, _ := codeModeShellObject(input, start)
	return call, call.cmd != ""
}

// Codex resolves a relative workdir against the turn directory.
func liveDiffWorkdir(workspace, workdir string) string {
	switch {
	case workdir == "":
		return workspace
	case filepath.IsAbs(workdir):
		return workdir
	case filepath.IsAbs(workspace):
		return filepath.Join(workspace, workdir)
	}
	return workspace
}
