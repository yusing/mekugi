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
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Preview state is router-lifetime only and never enters the replay store.
type liveDiffPreview struct {
	ID        string
	Workspace string
	Caller    string
	Thread    string
	Files     []mekugi.ReviewFile
	Input     string               // Display-only source, never executed.
	Syntax    []liveDiffSourceSpan `json:",omitempty"`
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
		preview.Files, preview.Syntax = nil, nil
		preview.DiffText, preview.Truncated = true, true
	}
	for preview.Input != "" && len(mustMarshalJSON(preview)) > limit {
		cut := max(1, len(preview.Input)/2)
		for cut < len(preview.Input) && !utf8.RuneStart(preview.Input[cut]) {
			cut++
		}
		preview.Input = preview.Input[cut:]
		preview.Syntax = liveDiffClipSyntax(preview.Syntax, cut)
		preview.Truncated = true
	}
	for preview.Input != "" && !utf8.RuneStart(preview.Input[0]) {
		preview.Input = preview.Input[1:]
	}
	return preview
}

func startLiveDiffPreview(ctx context.Context, broker *liveDiffBroker, workspace, thread string, kind ...string) *liveDiffPreviewWorker {
	ctx, cancel := context.WithCancel(ctx)
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
		worker.preview.Caller = t.commentaryAuthor
		if !t.subagentTurn {
			worker.preview.Caller = "/root"
		}
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
			worker.preview.Status = "STREAMING PREVIEW"
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
		delete(t.previews, itemID)
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

func (w *liveDiffPreviewWorker) run() {
	defer close(w.done)
	defer w.cancel()
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.broker.discardPreview(w.preview.ID)
		w.mu.Unlock()
	}()
	editRecognized := false
	codePatchHidden := false
	scriptVisible := false
	final := false
	for {
		if final {
			return
		}
		select {
		case <-w.ctx.Done():
		case <-w.wake:
		}
		// Coalesce bursts, but keep producing while the provider is still
		// streaming. No filesystem work runs on the provider forwarding path.
		timer := time.NewTimer(33 * time.Millisecond)
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
		if final && input == "" {
			w.broker.publishPreview(preview, false)
			continue
		}
		projectionInput := input
		shellDisplay := ""
		shellProvisional := false
		if w.kind == applyPatchToolName {
			if projected, ok := nativePatchPreview(input); ok {
				projected.Complete = final
				projected.ID, projected.Workspace, projected.Thread, projected.Caller = preview.ID, preview.Workspace, preview.Thread, preview.Caller
				w.mu.Lock()
				if !w.closed && (w.ctx.Err() == nil || final) {
					w.broker.publishPreview(projected, false)
				}
				w.mu.Unlock()
			}
			continue
		}
		if w.kind == nativeExecCommandToolName {
			var arguments map[string]json.RawMessage
			if json.Unmarshal([]byte(input), &arguments) != nil {
				continue
			}
			projectionInput = jsonString(arguments, "cmd")
			if directory := jsonString(arguments, "workdir"); filepath.IsAbs(directory) {
				preview.Workspace = directory
			}
		}
		if w.kind == "exec" {
			calls, ok := toolActivityUnwrapExecCalls(input, false)
			if !ok {
				patches := stockLiteralPatchInputs(input)
				if fragment := stockPatchFragment(input); fragment != "" {
					patches = append(patches, fragment)
				}
				if len(patches) != 0 {
					if projected, valid := nativePatchPreview(patches[len(patches)-1]); valid {
						projected.Complete = final
						projected.ID, projected.Workspace, projected.Thread, projected.Caller = preview.ID, preview.Workspace, preview.Thread, preview.Caller
						w.mu.Lock()
						if !w.closed && (w.ctx.Err() == nil || final) {
							w.broker.publishPreview(projected, false)
						}
						w.mu.Unlock()
						continue
					}
				}
				if strings.Contains(input, "*** Begin Patch") || stockPatchLiteralPresent(input) {
					if !codePatchHidden {
						w.broker.publishPreview(liveDiffPreview{ID: preview.ID}, true)
						codePatchHidden = true
					}
					continue
				}
				scripts, shellProgram := codeModeShellFragments(input)
				if len(scripts) != 0 {
					projectionInput = scripts[len(scripts)-1]
					shellDisplay = codeModeShellDisplay(scripts)
					shellProvisional = true
				} else {
					if shellProgram {
						// A batch prefix is not a JavaScript preview. Wait for a
						// literal command rather than flashing its unfinished wrapper.
						if scriptVisible {
							w.broker.publishPreview(liveDiffPreview{ID: preview.ID}, true)
							scriptVisible = false
						}
						continue
					}
					preview.Input, preview.Syntax, preview.Status = input, []liveDiffSourceSpan{{Path: "preview.js"}}, "STREAMING SCRIPT"
					w.mu.Lock()
					if !w.closed && (w.ctx.Err() == nil || final) && input != "" {
						w.broker.publishPreview(preview, false)
						scriptVisible = true
					}
					w.mu.Unlock()
					continue
				}
			}
			var scripts []string
			for _, call := range calls {
				if jsonString(call, "name") != nativeExecCommandToolName {
					continue
				}
				var arguments map[string]json.RawMessage
				if json.Unmarshal([]byte(jsonString(call, "arguments")), &arguments) == nil {
					scripts = append(scripts, jsonString(arguments, "cmd"))
				}
			}
			if len(scripts) != 0 && shellDisplay == "" {
				shellDisplay = codeModeShellDisplay(scripts)
			}
			for _, call := range slices.Backward(calls) {
				switch jsonString(call, "name") {
				case applyPatchToolName:
					if projected, ok := nativePatchPreview(jsonString(call, "input")); ok {
						projected.Complete = final
						projected.ID, projected.Workspace, projected.Thread, projected.Caller = preview.ID, preview.Workspace, preview.Thread, preview.Caller
						w.mu.Lock()
						if !w.closed && (w.ctx.Err() == nil || final) {
							w.broker.publishPreview(projected, false)
						}
						w.mu.Unlock()
						projectionInput = ""
					}
				case nativeExecCommandToolName:
					var arguments map[string]json.RawMessage
					if json.Unmarshal([]byte(jsonString(call, "arguments")), &arguments) == nil {
						projectionInput = jsonString(arguments, "cmd")
						if directory := jsonString(arguments, "workdir"); filepath.IsAbs(directory) {
							preview.Workspace = directory
						}
					}
				}
				if projectionInput != input {
					break
				}
			}
			if projectionInput == "" {
				continue
			}
			if projectionInput == input && !shellProvisional {
				preview.Input, preview.Syntax, preview.Status = input, []liveDiffSourceSpan{{Path: "preview.js"}}, "STREAMING SCRIPT"
				w.mu.Lock()
				if !w.closed && (w.ctx.Err() == nil || final) && input != "" {
					w.broker.publishPreview(preview, false)
				}
				w.mu.Unlock()
				continue
			}
		}
		if programs, err := shellsyntax.Split(projectionInput); err == nil {
			// Preview the current program, not earlier shell framing or edit payloads.
			if w.kind == "" {
				projectionInput = programs[len(programs)-1]
			}
		}
		if shellProvisional {
			preview.Input, preview.Syntax, preview.Status = shellDisplay, []liveDiffSourceSpan{{Path: "stream.sh"}}, "STREAMING SCRIPT"
			w.mu.Lock()
			if !w.closed && (w.ctx.Err() == nil || final) {
				w.broker.publishPreview(preview, false)
				scriptVisible = true
			}
			w.mu.Unlock()
			continue
		}
		statements, directory, partialLine, parsed := liveDiffShellStatements(projectionInput, preview.Workspace)
		ok := false
		projectionContext := w.ctx
		if final {
			// Accepted final input outlives transport teardown, but projection
			// remains independently bounded and never executes the command.
			projectionContext = context.WithoutCancel(projectionContext)
		}
		ctx, cancel := context.WithTimeout(projectionContext, time.Second)
		var files []mekugi.ReviewFile
		var err error
		if parsed {
			seenPaths := make(map[string]struct{})
			tainted := false
			for index, stmt := range statements {
				statementPartial := partialLine && index == len(statements)-1
				var projected []mekugi.ReviewFile
				var recognized bool
				projected, recognized, err = liveDiffShellWriteStatement(ctx, stmt, directory, statementPartial)
				if !recognized {
					if !liveDiffShellPreviewNeutral(stmt) {
						tainted = true
					}
					continue
				}
				ok = true
				if tainted {
					err = errors.New("streaming edit follows unsupported shell state")
					break
				}
				if err != nil {
					break
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
						err = errors.New("streaming edits depend on an earlier operation")
						break
					}
				}
				if err != nil {
					break
				}
				for path := range operationPaths {
					seenPaths[path] = struct{}{}
				}
				files = append(files, projected...)
			}
		}
		cancel()
		editRecognized = editRecognized || ok
		if editRecognized {
			scriptVisible = false
			preview.Input, preview.Syntax, preview.Files = "", nil, nil
			preview.Status = "STREAMING PREVIEW"
		} else {
			scriptVisible = true
			preview.Input, preview.Syntax = projectionInput, liveDiffScriptSyntax(projectionInput)
			if shellDisplay != "" {
				preview.Input, preview.Syntax = shellDisplay, []liveDiffSourceSpan{{Path: "stream.sh"}}
			} else if projection, projected := shellInterpreterScriptProjection(projectionInput); projected {
				preview.Input = projection.Source
				preview.Syntax = []liveDiffSourceSpan{{Path: liveDiffLanguagePath(projection.Language)}}
			}
			preview.Status = "STREAMING SCRIPT"
		}
		if ok && err == nil {
			preview.Files = files
			preview.Status = "STREAMING PREVIEW"
		}
		w.mu.Lock()
		// One preview is in flight, with only the latest input sampled next.
		// Final content and completion share one replaceable snapshot, so a slow
		// viewer cannot receive only removal after losing the last content frame.
		if !w.closed && (w.ctx.Err() == nil || final) && input != "" {
			w.broker.publishPreview(preview, false)
		}
		w.mu.Unlock()
	}
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
	if preview.Input != "" && !preview.DiffText && len(preview.Syntax) == 0 {
		preview.Syntax = liveDiffScriptSyntax(preview.Input)
	}
	preview = boundLiveDiffPreview(preview)
	if preview.Status == "STREAMING PREVIEW" && len(preview.Files) == 0 && preview.Input == "" {
		// Incomplete fragments retain the latest displayed projection, including
		// a bounded raw-diff window, never the preceding shell source.
		if previous := b.previews[preview.ID]; len(previous.Files) != 0 || previous.DiffText {
			preview.Files, preview.Input = previous.Files, previous.Input
			preview.DiffText, preview.Truncated = previous.DiffText, previous.Truncated
		}
	}
	if preview.Status == "STREAMING PREVIEW" && len(preview.Files) == 0 && preview.Input == "" {
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
