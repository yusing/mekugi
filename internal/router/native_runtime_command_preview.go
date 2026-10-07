package router

import (
	"strings"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

type runtimeCommandPreview struct {
	worker   *liveDiffPreviewWorker
	input    string
	complete bool
	settled  bool
}

// Native Bash uses the original asynchronous literal-shell projector, mailbox
// and renderer. No native command is transformed or executed by this adapter.
func (u *appServerUI) runtimeCommandPreview(e session.Event) {
	if e.Historical || e.CommandInput == nil || e.ID == "" {
		return
	}
	r := u.runtime
	u.reapRuntimeCommandPreviews()
	if r.previewBroker == nil {
		r.previewBroker = newLiveDiffBroker(u.ctx)
		r.previewBroker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{u.session.cwd: {u.thread: true}}})
		r.previewSubscriber = r.previewBroker.subscribe()
		r.previewReady = r.previewSubscriber.previewReady
		r.previewGap = r.previewSubscriber.gap
		r.commandPreviews = make(map[string]*runtimeCommandPreview)
	}
	p := r.commandPreviews[e.ID]
	if p == nil {
		if len(r.commandPreviews) >= 128 {
			return
		}
		worker := startLiveDiffPreview(u.ctx, r.previewBroker, u.session.cwd, u.thread, "Bash")
		worker.mu.Lock()
		worker.preview.ID, worker.preview.Caller = e.ID, "/root"
		if e.Caller != "" {
			worker.preview.Caller = "native/" + e.Caller
		}
		worker.mu.Unlock()
		p = &runtimeCommandPreview{worker: worker}
		r.commandPreviews[e.ID] = p
	}
	input := e.CommandInput
	if input.Complete {
		p.worker.finish(input.Text)
		p.complete = true
	} else if strings.HasPrefix(input.Text, p.input) {
		p.worker.appendDelta(input.Text[len(p.input):])
	}
	p.input = input.Text
}

func (u *appServerUI) applyRuntimeCommandPreviews() {
	r := u.runtime
	select {
	case <-r.previewGap:
		for id := range r.commandPreviews {
			u.withdrawRuntimeCommandPreview(id)
		}
		r.previewSubscriber = r.previewBroker.subscribe()
		r.previewReady, r.previewGap = r.previewSubscriber.previewReady, r.previewSubscriber.gap
	default:
	}
	for _, event := range r.previewBroker.takePreviews(r.previewSubscriber) {
		if event.Preview == nil {
			continue
		}
		preview := *event.Preview
		if caller, ok := strings.CutPrefix(preview.Caller, "native/"); ok {
			preview.Caller = u.runtimeCallerLane(caller)
		}
		preview.Footer = "Proposed input · not saved edit evidence"
		u.shell.preview(preview)
		u.shell.diff.previewPane.Update(preview)
		pending := r.commandPreviews[preview.ID]
		if len(preview.Files) != 0 && pending != nil && !pending.complete && !u.shell.diff.modeChosen {
			u.shell.diff.diffMode = false
		}
		if preview.Complete && pending != nil && pending.settled {
			delete(r.commandPreviews, preview.ID)
		}
		u.shell.diff.dirty, u.dirty = true, true
	}
}

func (u *appServerUI) finishRuntimeCommandPreview(id string) {
	if p := u.runtime.commandPreviews[id]; p != nil {
		p.settled = true
		if !p.complete {
			p.worker.stop()
			u.withdrawRuntimeCommandPreview(id)
			delete(u.runtime.commandPreviews, id)
		} else if view := u.shell.diff.previewPane.Views[id]; view != nil && view.Current.Complete {
			delete(u.runtime.commandPreviews, id)
		}
	}
	u.reapRuntimeCommandPreviews()
}

// Ordinary Bash can finish without a proposal. Drain any final frame before
// releasing its registration, regardless of whether projection made a card.
func (u *appServerUI) reapRuntimeCommandPreviews() {
	for id, p := range u.runtime.commandPreviews {
		if !p.settled {
			continue
		}
		select {
		case <-p.worker.done:
			u.applyRuntimeCommandPreviews()
			delete(u.runtime.commandPreviews, id)
		default:
		}
	}
}

func (u *appServerUI) withdrawRuntimeCommandPreview(id string) {
	delete(u.shell.livePending, id)
	withdrawal := diffview.Preview{ID: id, Workspace: u.session.cwd}
	u.shell.liveDock.Update(withdrawal)
	u.shell.diff.previewPane.Update(withdrawal)
	u.shell.diff.dirty, u.dirty = true, true
}

func (u *appServerUI) closeRuntimeCommandPreviews() {
	r := u.runtime
	for id, p := range r.commandPreviews {
		p.worker.stop()
		u.withdrawRuntimeCommandPreview(id)
	}
	if r.previewBroker != nil {
		r.previewBroker.unsubscribe(r.previewSubscriber)
	}
	r.commandPreviews, r.previewBroker, r.previewSubscriber, r.previewReady = nil, nil, nil, nil
	r.previewGap = nil
}
