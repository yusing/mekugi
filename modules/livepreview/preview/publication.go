package preview

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Large previews retain a bounded suffix of the actual unified diff.
func boundPreview(preview Preview) Preview {
	const limit = 48 << 10
	if len(responses.MustMarshalJSON(preview)) <= limit {
		return preview
	}
	if len(preview.Files) != 0 {
		var tail string
		for index := len(preview.Files) - 1; index >= 0 && len(tail) < 16<<10; index-- {
			file := preview.Files[index]
			if file.BeforePath != "" && file.AfterPath == "" {
				continue // Deleted files do not have live source cards.
			}
			diff := file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath))
			if len(diff) > 16<<10 {
				diff = diff[len(diff)-(16<<10):]
			}
			tail = diff + tail
		}
		preview.Input = tail
		preview.Files = nil
		if tail == "" {
			preview.Status = "" // Withdraw a deletion-only oversized preview.
			return preview
		}
		preview.DiffText, preview.Truncated = true, true
	}
	for preview.Input != "" && len(responses.MustMarshalJSON(preview)) > limit {
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

// Cleanup only live state. An unconditional removal would overwrite an atomic
// final snapshot still queued for a slow viewer with a contentless marker.
func (b *Session) Discard(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.removePreviewLocked(id) {
		b.emitPreviewLocked(Preview{ID: id})
	}
}

func (b *Session) Update(preview Preview, remove bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remove {
		b.removePreviewLocked(preview.ID)
		b.emitPreviewLocked(Preview{ID: preview.ID})
		return
	}
	if turn, known := b.previewTurns[preview.Thread]; known && preview.Turn != "" && (turn.id != preview.Turn || turn.complete) {
		return
	}
	if !b.scope.Workspaces[preview.Workspace][preview.Thread] {
		return
	}
	at := slices.IndexFunc(b.previews, func(old Preview) bool { return old.ID == preview.ID })
	if at < 0 && !preview.Complete && len(b.previews) >= 16 {
		return
	}
	// Only source hunks can open live cards. Metadata and capture failures
	// remain in the saved evidence, not a transcript-replacing empty preview.
	preview.Files = slices.DeleteFunc(slices.Clone(preview.Files), func(file diffmodel.ReviewFile) bool {
		added, removed := file.LineCounts()
		return file.Binary || file.Link || added < 0 || removed < 0 || added+removed == 0
	})
	preview = boundPreview(preview)
	if preview.Status == PreviewEdit && len(preview.Files) == 0 && (preview.Input == "" || preview.Input == "\n") {
		// Incomplete fragments retain the latest displayed projection, including
		// a bounded raw-diff window, never the preceding shell source. A lone
		// newline is the pending stock-patch marker, not a new blank edit.
		if previous, _ := b.previewLocked(preview.ID); len(previous.Files) != 0 || previous.DiffText {
			preview.Files, preview.Input, preview.Tool = previous.Files, previous.Input, previous.Tool
			preview.DiffText, preview.Truncated = previous.DiffText, previous.Truncated
		}
	}
	if len(preview.Files) == 0 && strings.TrimSpace(preview.Input) == "" {
		// Neither a patch header nor a status establishes a displayable hunk.
		b.removePreviewLocked(preview.ID)
		preview.Status, preview.Input = "", ""
		b.emitPreviewLocked(preview)
		return
	}
	if preview.Complete {
		b.removePreviewLocked(preview.ID)
		if preview.Evaluated {
			b.completedPreviews = slices.DeleteFunc(b.completedPreviews, func(old Preview) bool {
				return old.ID == preview.ID
			})
			if len(b.completedPreviews) == 16 {
				b.completedPreviews = slices.Delete(b.completedPreviews, 0, 1)
			}
			b.completedPreviews = append(b.completedPreviews, preview)
		}
	} else if at >= 0 {
		b.previews[at] = preview
	} else {
		b.previews = append(b.previews, preview)
	}
	b.emitPreviewLocked(preview)
}

type previewTurn struct {
	id       string
	complete bool
}

func (b *Session) SetTurn(thread, turn string, active bool) {
	if b == nil || thread == "" || turn == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.previewTurns == nil {
		b.previewTurns = make(map[string]previewTurn)
	}
	previous, known := b.previewTurns[thread]
	if !active && known && previous.id != turn {
		return
	}
	if active && known && previous.id == turn {
		return
	}
	b.previewTurns[thread] = previewTurn{id: turn, complete: !active}
	stale := func(p Preview) bool {
		return p.Thread == thread && p.Turn != "" && (p.Turn != turn || !active)
	}
	retired := make(map[string]Preview)
	b.previews = slices.DeleteFunc(b.previews, func(preview Preview) bool {
		if !stale(preview) {
			return false
		}
		retired[preview.ID] = preview
		return true
	})
	for _, preview := range b.completedPreviews {
		if stale(preview) {
			retired[preview.ID] = preview
		}
	}
	// A finished input frame may already be queued but not yet displayed.
	// Replace it too, or draining the mailbox could reopen the interrupted dock.
	if b.subscriber != nil {
		for _, event := range b.subscriber.previews {
			preview := *event.Preview
			if stale(preview) {
				retired[preview.ID] = preview
			}
		}
	}
	b.completedPreviews = slices.DeleteFunc(b.completedPreviews, stale)
	for id, preview := range retired {
		b.emitPreviewLocked(Preview{ID: id, Workspace: preview.Workspace, Thread: thread, Turn: preview.Turn})
	}
}

// A running card vanishes when it has no observed effect. Unlike completed
// input streams, it must not leave a completed placeholder behind.
func (b *Session) DiscardRunning(preview Preview) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.removePreviewLocked(preview.ID) {
		b.emitPreviewLocked(Preview{ID: preview.ID, Workspace: preview.Workspace, Thread: preview.Thread})
	}
}
