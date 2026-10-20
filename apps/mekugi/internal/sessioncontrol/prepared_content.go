package sessioncontrol

import (
	"slices"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

const preparedContentBytes = 256 << 10
const preparedTotalBytes = 8 << 20

type preparedContent struct {
	thread string
	activity.PreparedContent
	bytes int
}

// ObservePrepared retains auxiliary display content without persistent storage.
// Oversized replacements retire older observations rather than show stale text.
func (a *Audience) ObservePrepared(thread string, content activity.PreparedContent) {
	if a == nil || content.ID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if node := a.threads[thread]; a.closed || node == nil || node.conflicted {
		return
	}
	a.prepared = slices.DeleteFunc(a.prepared, func(old preparedContent) bool {
		if old.thread != thread || old.ID != content.ID {
			return false
		}
		a.preparedBytes -= old.bytes
		return true
	})
	if len(content.Text) > preparedContentBytes || len(content.ID) > MaxActivityMetadataBytes || len(content.Calls) > 256 {
		return
	}
	size := len(content.ID) + len(content.Text) + len(thread)
	metadata := len(content.ID)
	for _, call := range content.Calls {
		size += len(call)
		metadata += len(call)
		if len(call) > MaxActivityMetadataBytes {
			return
		}
	}
	if metadata > MaxActivityMetadataBytes {
		return
	}
	for len(a.prepared) >= 256 || a.preparedBytes+size > preparedTotalBytes {
		a.preparedBytes -= a.prepared[0].bytes
		a.prepared = slices.Delete(a.prepared, 0, 1)
	}
	content.Calls = slices.Clone(content.Calls)
	a.prepared = append(a.prepared, preparedContent{thread, content, size})
	a.preparedBytes += size
}

// Prepared resolves only the currently attached, authenticated audience.
func (a *Audience) Prepared(root, thread, id string) (activity.PreparedContent, bool) {
	if a == nil {
		return activity.PreparedContent{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || !a.supplements.Attached(root) || a.rootLocked(thread) != root {
		return activity.PreparedContent{}, false
	}
	for _, content := range slices.Backward(a.prepared) {
		if content.thread == thread && (content.ID == id || slices.Contains(content.Calls, id)) {
			result := content.PreparedContent
			result.Calls = slices.Clone(result.Calls)
			return result, true
		}
	}
	return activity.PreparedContent{}, false
}
