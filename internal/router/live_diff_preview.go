package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Preview status names what a card shows; the viewer renders the state.
const (
	liveDiffPreviewEdit        = "edit"
	liveDiffPreviewRunning     = "running"
	liveDiffPreviewPending     = "pending"
	liveDiffPreviewUnavailable = "unavailable: "
)

// Preview state is router-lifetime only and never enters the replay store.
type liveDiffPreview struct {
	ID        string
	Workspace string
	Caller    string
	Thread    string
	Files     []mekugi.ReviewFile
	Input     string // Display-only text, never executed.
	Truncated bool
	Evaluated bool `json:",omitzero"`
	Complete  bool `json:",omitzero"`
	DiffText  bool `json:",omitzero"`
	Status    string
	Footer    string `json:",omitempty"`
}

type liveDiffPreviewWorker struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	broker    *liveDiffBroker
	preview   liveDiffPreview
	input     strings.Builder
	wake      chan struct{}
	done      chan struct{}
	closed    bool
	finishing bool
	kind      string
}

// Large previews retain a bounded suffix of the actual unified diff.
func boundLiveDiffPreview(preview liveDiffPreview) liveDiffPreview {
	const limit = 48 << 10
	if len(mustMarshalJSON(preview)) <= limit {
		return preview
	}
	if len(preview.Files) != 0 {
		var tail string
		for index := len(preview.Files) - 1; index >= 0 && len(tail) < 16<<10; index-- {
			diff := preview.Files[index].UnifiedDiff()
			if len(diff) > 16<<10 {
				diff = diff[len(diff)-(16<<10):]
			}
			tail = diff + tail
		}
		preview.Input = tail
		preview.Files = nil
		preview.DiffText, preview.Truncated = true, true
	}
	for preview.Input != "" && len(mustMarshalJSON(preview)) > limit {
		cut := max(1, len(preview.Input)/2)
		for cut < len(preview.Input) && !utf8.RuneStart(preview.Input[cut]) {
			cut++
		}
		preview.Input = preview.Input[cut:]
		preview.Truncated = true
	}
	for preview.Input != "" && !utf8.RuneStart(preview.Input[0]) {
		preview.Input = preview.Input[1:]
	}
	return preview
}

func startLiveDiffPreview(ctx context.Context, broker *liveDiffBroker, workspace, thread string, kind ...string) *liveDiffPreviewWorker {
	ctx, cancel := context.WithCancel(withLiveDiffSources(ctx))
	worker := &liveDiffPreviewWorker{
		ctx: ctx, cancel: cancel, broker: broker,
		preview: liveDiffPreview{ID: rand.Text(), Workspace: workspace, Thread: thread},
		wake:    make(chan struct{}, 1), done: make(chan struct{}),
	}
	if len(kind) != 0 {
		worker.kind = kind[0]
	}
	go worker.run()
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
	auto := t.proxy.autoLiveDiff
	if auto == nil || !auto.enabled.Load() {
		return
	}
	if t.previews == nil {
		t.previews = make(map[string]*liveDiffPreviewWorker)
	}
	worker := t.previews[itemID]
	if worker == nil {
		auto.requestLaunch(t.directory, t.threadID)
		worker = startLiveDiffPreview(t.ctx, auto.events, t.directory, t.threadID, kind)
		worker.mu.Lock()
		worker.preview.Caller = t.operationCaller()
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
			worker.preview.Status = liveDiffPreviewEdit
			worker.broker.publishPreview(worker.preview, false)
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
		worker.broker.discardPreview(worker.preview.ID)
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
	worker.broker.discardPreview(worker.preview.ID)
}

func (w *liveDiffPreviewWorker) projectStockPreview(input, workspace string, final bool) (liveDiffPreview, bool) {
	preview, ok := nativePatchPreview(input)
	if !ok {
		return liveDiffPreview{}, false
	}
	projectionContext := w.ctx
	if final {
		projectionContext = context.WithoutCancel(projectionContext)
	}
	ctx, cancel := context.WithTimeout(projectionContext, time.Second)
	defer cancel()
	projected := projectStockPatchPreview(ctx, workspace, preview)
	if !final && strings.HasPrefix(projected.Status, liveDiffPreviewUnavailable) && !strings.HasSuffix(input, "\n") {
		// Try the completed prefix only after the live suffix fails. Valid
		// partial additions remain visible as they stream, while unfinished
		// context or control lines cannot invalidate the last usable preview.
		if end := strings.LastIndexByte(input, '\n'); end >= 0 {
			preview.Input = input[:end+1]
			projected = projectStockPatchPreview(ctx, workspace, preview)
		}
	}
	return projected, true
}

func (w *liveDiffPreviewWorker) run() {
	defer close(w.done)
	defer w.cancel()
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.broker.discardPreview(w.preview.ID)
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
		timer := time.NewTimer(liveDiffPreviewFrameDelay)
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
		// Provider deltas arrive in bursts. Reveal the received input at a steady
		// pace instead of jumping per burst. A completed call waits briefly for the
		// reveal to catch up; a cancelled transport shows its final input at once.
		shown := len(input)
		if w.ctx.Err() == nil {
			shown = pacer.advance(input, final, encoded)
		}
		backlog = shown < len(input)
		if backlog {
			input, final, preview.Complete = input[:shown], false, false
		}
		if !final && shown == revealed {
			continue
		}
		revealed = shown
		if final && input == "" {
			w.broker.publishPreview(preview, false)
			continue
		}
		projected, recognized := w.project(input, preview.Workspace, final)
		edited = edited || recognized
		if !edited {
			continue
		}
		if !recognized {
			// A retained projection stays until a later frame replaces it.
			projected = liveDiffPreview{Status: liveDiffPreviewEdit}
		}
		projected.Complete = final
		projected.ID, projected.Workspace, projected.Thread, projected.Caller = preview.ID, preview.Workspace, preview.Thread, preview.Caller
		w.mu.Lock()
		if final && projected.Status == liveDiffPreviewUnavailable+"patch cannot be projected" {
			// A speculative patch projection cannot establish failure or success.
			// Clear any earlier preview and leave the result to the host tool.
			w.broker.publishPreview(liveDiffPreview{
				ID: preview.ID, Workspace: preview.Workspace, Thread: preview.Thread, Complete: true,
			}, false)
			w.mu.Unlock()
			continue
		}
		// One preview is in flight, with only the latest input sampled next.
		// Final content and completion share one replaceable snapshot, so a slow
		// viewer cannot receive only removal after losing the last content frame.
		if !w.closed && (w.ctx.Err() == nil || final) && (final || !strings.HasPrefix(projected.Status, liveDiffPreviewUnavailable)) {
			w.broker.publishPreview(projected, false)
		}
		w.mu.Unlock()
	}
}

// project recognizes the edit in a call's received input. Command and script
// text that is not an edit has no preview.
func (w *liveDiffPreviewWorker) project(input, workspace string, final bool) (liveDiffPreview, bool) {
	shell := func(call codeModeShellCall) (liveDiffPreview, bool) {
		files, recognized, err := w.projectShell(call.cmd, liveDiffWorkdir(workspace, call.workdir), final)
		switch {
		case recognized && call.dynamicWorkdir:
			return liveDiffPreview{Status: liveDiffPreviewUnavailable + "edit target depends on a computed workdir"}, true
		case !recognized || err != nil:
			return liveDiffPreview{}, recognized
		}
		return liveDiffPreview{Files: files, Status: liveDiffPreviewEdit}, true
	}
	switch w.kind {
	case applyPatchToolName:
		return w.projectStockPreview(input, workspace, final)
	case nativeExecCommandToolName:
		call, ok := liveDiffExecArguments(input)
		if !ok {
			return liveDiffPreview{}, false
		}
		return shell(call)
	case "exec":
		if calls, ok := toolActivityUnwrapExecCalls(input, false); ok {
			for _, call := range slices.Backward(calls) {
				switch jsonString(call, "name") {
				case applyPatchToolName:
					return w.projectStockPreview(jsonString(call, "input"), workspace, final)
				case nativeExecCommandToolName:
					var arguments map[string]json.RawMessage
					if json.Unmarshal([]byte(jsonString(call, "arguments")), &arguments) != nil {
						return liveDiffPreview{}, false
					}
					return shell(codeModeShellCall{cmd: jsonString(arguments, "cmd"), workdir: jsonString(arguments, "workdir")})
				}
			}
			return liveDiffPreview{}, false
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
			return liveDiffPreview{}, false
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
		call := codeModeShellCall{cmd: jsonString(arguments, "cmd"), workdir: jsonString(arguments, "workdir")}
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

// projectShell predicts literal shell and interpreter writes without running
// the command. Recognized reports an edit even when its projection failed.
func (w *liveDiffPreviewWorker) projectShell(program, directory string, final bool) ([]mekugi.ReviewFile, bool, error) {
	statements, directory, partialLine, parsed := liveDiffShellStatements(program, directory)
	if !parsed {
		return nil, false, nil
	}
	projectionContext := w.ctx
	if final {
		// Accepted final input outlives transport teardown, but projection
		// remains independently bounded and never executes the command.
		projectionContext = context.WithoutCancel(projectionContext)
	}
	ctx, cancel := context.WithTimeout(projectionContext, time.Second)
	defer cancel()
	var files []mekugi.ReviewFile
	recognized := false
	seenPaths := make(map[string]struct{})
	tainted := false
	for index, stmt := range statements {
		statementPartial := partialLine && index == len(statements)-1
		projected, ok, err := liveDiffShellWriteStatement(ctx, stmt, directory, statementPartial)
		if !ok {
			if !liveDiffShellPreviewNeutral(stmt) {
				tainted = true
			}
			continue
		}
		recognized = true
		if tainted {
			return nil, true, errors.New("edit follows unsupported shell state")
		}
		if err != nil {
			return nil, true, err
		}
		operationPaths := make(map[string]struct{})
		for _, file := range projected {
			if file.BeforePath != "" {
				operationPaths[file.BeforePath] = struct{}{}
			}
			if file.AfterPath != "" {
				operationPaths[file.AfterPath] = struct{}{}
			}
		}
		for path := range operationPaths {
			if _, exists := seenPaths[path]; exists {
				return nil, true, errors.New("edits depend on an earlier operation")
			}
		}
		for path := range operationPaths {
			seenPaths[path] = struct{}{}
		}
		files = append(files, projected...)
	}
	return files, recognized, nil
}

// Cleanup only live state. An unconditional removal would overwrite an atomic
// final snapshot still queued for a slow viewer with a contentless marker.
func (b *liveDiffBroker) discardPreview(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, active := b.previews[id]; active {
		delete(b.previews, id)
		b.emitPreviewLocked(liveDiffPreview{ID: id})
	}
}

func (b *liveDiffBroker) publishPreview(preview liveDiffPreview, remove bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remove {
		delete(b.previews, preview.ID)
		b.emitPreviewLocked(liveDiffPreview{ID: preview.ID})
		return
	}
	if !b.scope.Workspaces[preview.Workspace][preview.Thread] {
		return
	}
	if b.previews == nil {
		b.previews = make(map[string]liveDiffPreview)
	}
	if _, exists := b.previews[preview.ID]; !exists && !preview.Complete && len(b.previews) >= 16 {
		return
	}
	preview = boundLiveDiffPreview(preview)
	if preview.Status == liveDiffPreviewEdit && len(preview.Files) == 0 && preview.Input == "" {
		// Incomplete fragments retain the latest displayed projection, including
		// a bounded raw-diff window, never the preceding shell source.
		if previous := b.previews[preview.ID]; len(previous.Files) != 0 || previous.DiffText {
			preview.Files, preview.Input = previous.Files, previous.Input
			preview.DiffText, preview.Truncated = previous.DiffText, previous.Truncated
		}
	}
	if preview.Status == liveDiffPreviewEdit && len(preview.Files) == 0 && preview.Input == "" {
		// An unfinished edit is not an error panel or a raw-script preview.
		// Wait for a real projection while leaving captured history untouched.
		delete(b.previews, preview.ID)
		preview.Status = ""
		b.emitPreviewLocked(preview)
		return
	}
	if preview.Complete {
		delete(b.previews, preview.ID)
		if preview.Evaluated {
			b.completedPreviews = slices.DeleteFunc(b.completedPreviews, func(old liveDiffPreview) bool {
				return old.ID == preview.ID
			})
			if len(b.completedPreviews) == 16 {
				b.completedPreviews = slices.Delete(b.completedPreviews, 0, 1)
			}
			b.completedPreviews = append(b.completedPreviews, preview)
		}
	} else {
		b.previews[preview.ID] = preview
	}
	b.emitPreviewLocked(preview)
}
