package composer

import (
	"crypto/sha256"
	"slices"

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
}

func (a ContextAttachment) Matches(snapshot attachments.Snapshot, original string) bool {
	return snapshot.ID != "" && snapshot.ID == a.ID || snapshot.ID == "" && sha256.Sum256([]byte(original)) == a.Original
}

// AttachmentContext caches one native context window in the owning thread scope.
// Native history remains the durable source for resume, fork and rewind.
type AttachmentContext struct {
	window string
	values []ContextAttachment
}

func (c *AttachmentContext) Get(window string) ([]ContextAttachment, bool) {
	if c.window != window {
		return nil, false
	}
	return cloneContextAttachments(c.values), true
}

func (c *AttachmentContext) Set(window string, values []ContextAttachment) {
	c.window, c.values = window, cloneContextAttachments(values)
}

func cloneContextAttachments(values []ContextAttachment) []ContextAttachment {
	copy := slices.Clone(values)
	for i := range copy {
		copy[i].Placeholders = slices.Clone(copy[i].Placeholders)
		copy[i].Selection.Frames = slices.Clone(copy[i].Selection.Frames)
		copy[i].Skills = slices.Clone(copy[i].Skills)
	}
	return copy
}
