package launch

import "github.com/yusing/mekugi/modules/conversation/composer"

// imageClaims borrows the actual pending operation/effect values. ownedImages
// remains the sole deletion authority; a publication never grants ownership of
// a client-supplied path. Clients copy restored images before acknowledging.
func (n *nativeViews) imageClaims(yield func(string)) {
	claim := func(draft *composer.Draft) {
		if draft == nil {
			return
		}
		for _, image := range draft.Images {
			yield(image.Path)
		}
		for _, path := range draft.AnswerImages {
			yield(path)
		}
	}
	for _, view := range n.views {
		if view.ctx.Err() != nil {
			continue
		}
		claim(view.picker.sideTask)
		for _, effect := range view.effects.pending {
			claim(effect.Restore)
			claim(effect.ShellRestore)
		}
	}
}
