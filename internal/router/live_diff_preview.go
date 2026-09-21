package router

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

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
}

type liveDiffPreviewWorker struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	broker  *liveDiffBroker
	preview liveDiffPreview
	input   strings.Builder
	wake    chan struct{}
	done    chan struct{}
	closed  bool
}

func startLiveDiffPreview(ctx context.Context, broker *liveDiffBroker, workspace, thread string) *liveDiffPreviewWorker {
	ctx, cancel := context.WithCancel(ctx)
	worker := &liveDiffPreviewWorker{
		ctx: ctx, cancel: cancel, broker: broker,
		preview: liveDiffPreview{ID: rand.Text(), Workspace: workspace, Thread: thread},
		wake:    make(chan struct{}, 1), done: make(chan struct{}),
	}
	go worker.run()
	return worker
}

func (t *mekugiResponseTransform) previewDelta(itemID, delta string) {
	pending, ok := t.pending[itemID]
	if !ok || pending.toolName != "shell" || delta == "" {
		return
	}
	if _, complete := t.local[pending.callID]; complete {
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
		worker = startLiveDiffPreview(t.ctx, auto.events, t.directory, t.threadID)
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
	if !worker.closed {
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

func (worker *liveDiffPreviewWorker) stop() {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.closed = true
	worker.cancel()
	worker.broker.publishPreview(worker.preview, true)
}

func (w *liveDiffPreviewWorker) run() {
	defer close(w.done)
	editRecognized := false
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}
		// Coalesce bursts, but keep producing while the provider is still
		// streaming. No filesystem work runs on the provider forwarding path.
		timer := time.NewTimer(33 * time.Millisecond)
		select {
		case <-w.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		select {
		case <-w.wake:
		default:
		}
		w.mu.Lock()
		input := w.input.String()
		preview := w.preview
		w.mu.Unlock()
		projectionInput := input
		if programs, err := shellsyntax.Split(input); err == nil {
			// Preview the current program, not earlier shell framing or edit payloads.
			projectionInput = programs[len(programs)-1]
		}
		statements, directory, partialLine, parsed := liveDiffShellStatements(projectionInput, preview.Workspace)
		ok := false
		ctx, cancel := context.WithTimeout(w.ctx, time.Second)
		var files []mekugi.ReviewFile
		var err error
		if parsed {
			seenPaths := make(map[string]struct{})
			tainted := false
			for index, stmt := range statements {
				statementPartial := partialLine && index == len(statements)-1
				var projected []mekugi.ReviewFile
				var recognized bool
				if edits, _, edit := liveDiffShellEditStatement(stmt, directory, statementPartial); edit {
					recognized = true
					if !tainted {
						projected, err = mekugi.PreviewForHostAt(ctx, directory, edits)
					}
				} else {
					projected, recognized, err = liveDiffShellWriteStatement(ctx, stmt, directory, statementPartial)
				}
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
		editRecognized = editRecognized || ok || liveDiffShellComposedHpatch(projectionInput)
		if editRecognized {
			preview.Input, preview.Syntax, preview.Files = "", nil, nil
			preview.Status = "STREAMING PREVIEW"
		} else {
			preview.Input, preview.Syntax = projectionInput, liveDiffScriptSyntax(projectionInput)
			if projection, projected := shellInterpreterScriptProjection(projectionInput); projected {
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
		// New deltas must not starve visible progress; completion cancels output.
		if !w.closed && w.ctx.Err() == nil && input != "" {
			w.broker.publishPreview(preview, false)
		}
		w.mu.Unlock()
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
