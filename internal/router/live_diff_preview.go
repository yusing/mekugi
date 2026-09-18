package router

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi"
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
			worker.preview.Status = "PREVIEW UNAVAILABLE: input exceeds 256 KiB"
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
		preview.Input, preview.Syntax = input, liveDiffScriptSyntax(input)
		preview.Status = "STREAMING SCRIPT"
		if script, directory, ok := liveDiffShellEdit(input, preview.Workspace); ok {
			ctx, cancel := context.WithTimeout(w.ctx, time.Second)
			files, err := mekugi.PreviewForHostAt(ctx, directory, script)
			cancel()
			if err == nil && len(files) != 0 {
				preview.Files, preview.Input, preview.Syntax = files, "", nil
				preview.Status = "STREAMING PREVIEW"
			}
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
	if _, exists := b.previews[preview.ID]; !exists && len(b.previews) >= 16 {
		return
	}
	if preview.Input != "" && len(preview.Syntax) == 0 {
		preview.Syntax = liveDiffScriptSyntax(preview.Input)
	}
	// Bound retained preview payloads independently of durable edit evidence.
	for preview.Input != "" && len(mustMarshalJSON(preview)) > 48<<10 {
		cut := max(1, len(preview.Input)/2)
		for cut < len(preview.Input) && !utf8.RuneStart(preview.Input[cut]) {
			cut++
		}
		preview.Input = preview.Input[cut:]
		preview.Syntax = liveDiffClipSyntax(preview.Syntax, cut)
		preview.Truncated = true
	}
	if preview.Truncated {
		preview.Input = strings.Clone(preview.Input)
	}
	if len(mustMarshalJSON(preview)) > 48<<10 {
		if _, exists := b.previews[preview.ID]; exists {
			return // Keep the last useful frame instead of flickering to an error.
		}
		preview.Files = nil
		preview.Status = "PREVIEW UNAVAILABLE: source exceeds 48 KiB"
	}
	b.previews[preview.ID] = preview
	b.emitPreviewLocked(preview)
}
