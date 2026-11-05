package composer

import (
	"crypto/sha256"
	"slices"
	"sync"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// ContextAttachment preserves submitted identities and selected bytes. File and
// skill bodies are excluded: files are recalled explicitly and skills refreshed.
type ContextAttachment struct {
	ID           string
	Original     [32]byte
	Placeholders []string
	Selection    attachments.Snapshot
	Skills       []Token
	Images       []Token
}

// AttachmentSnapshot borrows the active window's optional recall publication.
// Source identities and bytes remain detached from the mutable cache.
type AttachmentSnapshot struct {
	Values []ContextAttachment
	Recall *AttachmentRecall
}

// AttachmentRecall caches the selected reader and frozen evidence hints for one
// active native window.
// Reopening history may issue fresh handles; existing handles remain retained.
type AttachmentRecall struct {
	mu    sync.Mutex
	hints map[string]string
}

func (r *AttachmentRecall) Publish(key string, create func() (string, error)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hint, ok := r.hints[key]; ok {
		return hint, nil
	}
	hint, err := create()
	if err == nil {
		if r.hints == nil {
			r.hints = make(map[string]string)
		}
		r.hints[key] = hint
	}
	return hint, err
}

func (a ContextAttachment) Matches(snapshot attachments.Snapshot, original string) bool {
	return snapshot.ID != "" && snapshot.ID == a.ID || snapshot.ID == "" && sha256.Sum256([]byte(original)) == a.Original
}

// AttachmentContext caches one native context window in the owning thread scope.
// Native history remains the durable source for resume, fork and rewind.
type AttachmentContext struct {
	window string
	values []ContextAttachment
	recall *AttachmentRecall
}

func (c *AttachmentContext) Get(window string) (AttachmentSnapshot, bool) {
	if c.window != window {
		return AttachmentSnapshot{}, false
	}
	return AttachmentSnapshot{Values: cloneContextAttachments(c.values), Recall: c.recall}, true
}

func (c *AttachmentContext) Set(window string, values []ContextAttachment) {
	c.window, c.values = window, cloneContextAttachments(values)
	c.recall = &AttachmentRecall{}
}

func cloneContextAttachments(values []ContextAttachment) []ContextAttachment {
	copy := slices.Clone(values)
	for i := range copy {
		copy[i].Placeholders = slices.Clone(copy[i].Placeholders)
		copy[i].Selection.Frames = slices.Clone(copy[i].Selection.Frames)
		copy[i].Skills = slices.Clone(copy[i].Skills)
		copy[i].Images = slices.Clone(copy[i].Images)
	}
	return copy
}
