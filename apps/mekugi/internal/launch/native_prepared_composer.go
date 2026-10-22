package launch

import (
	"context"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// composerPreparation borrows one request's off-loop acquisition. Shared command
// admission decides when its files enter the existing queued-input image owner.
// The legacy local path supplies nil and retains its established acquisition.
type composerPreparation struct {
	ctx   context.Context
	input *sessionapi.PreparedInput
	side  bool // Admission bound this input to the actual Side owner.
}

func (p *composerPreparation) admit(draft composer.Draft, owned *map[string]bool) bool {
	if p == nil {
		return true
	}
	if p.input == nil {
		return len(draft.Images)+len(draft.Files)+len(draft.Skills)+len(draft.Selections) == 0
	}
	if p.input.Admitted() {
		return true
	}
	if *owned == nil {
		*owned = make(map[string]bool)
	}
	return p.input.Admit(p.ctx, *owned)
}

func (p *composerPreparation) snapshot(draft composer.Draft) composer.Draft {
	if p != nil && p.input != nil {
		draft.Attachments = slices.Clone(p.input.Draft.Attachments)
		draft.AttachmentNotice = p.input.Draft.AttachmentNotice
		draft.AnswerImages = slices.Clone(p.input.Draft.AnswerImages)
	}
	return draft
}

func (p *composerPreparation) take(editor *composer.Editor) composer.Draft {
	return p.snapshot(editor.Take())
}
