package launch

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type nativeRestoredEffect struct {
	attachment string
	effect     sessionapi.NativeEffect
	owned      []string
	err        error
	complete   bool
}

func (r nativeRestoredEffect) cleanup() {
	for _, path := range r.owned {
		_ = os.Remove(path)
	}
}

func copyRestoredEffect(ctx context.Context, effect sessionapi.NativeEffect) (result nativeRestoredEffect) {
	result.effect = effect
	result.complete = true
	copies := make(map[string]string)
	copyDraft := func(source *composer.Draft) *composer.Draft {
		if source == nil {
			return nil
		}
		draft := source.Clone()
		acquire := func(path string) string {
			if result.err != nil {
				return path
			}
			if copied := copies[path]; copied != "" {
				return copied
			}
			copied, err := snapshotNativeImage(ctx, path)
			if err != nil {
				result.err = err
				return path
			}
			copies[path] = copied
			result.owned = append(result.owned, copied)
			return copied
		}
		for i := range draft.Images {
			draft.Images[i].Path = acquire(draft.Images[i].Path)
		}
		for i := range draft.AnswerImages {
			draft.AnswerImages[i] = acquire(draft.AnswerImages[i])
		}
		return &draft
	}
	result.effect.Restore = copyDraft(effect.Restore)
	result.effect.ShellRestore = copyDraft(effect.ShellRestore)
	if result.err == nil {
		result.err = ctx.Err()
	}
	if result.err != nil {
		result.cleanup()
		result.owned = nil
	}
	return result
}

// A server image claim is released only after its bytes belong to this client.
// Failed acquisition pauses the receipt watermark; the next user interaction
// retries acquisition, never the already admitted submission.
func (c *nativeClient) restoreEffectImages(effect sessionapi.NativeEffect) (sessionapi.NativeEffect, bool) {
	hasImages := func(draft *composer.Draft) bool { return draft != nil && len(draft.Images)+len(draft.AnswerImages) > 0 }
	if !hasImages(effect.Restore) && !hasImages(effect.ShellRestore) {
		return effect, true
	}
	if result := c.restoration; result != nil {
		if !result.complete || result.err != nil {
			return effect, false
		}
		if result.attachment != c.event.Attachment || result.effect.ID != effect.ID {
			result.cleanup()
			c.restoration = nil
			return effect, false
		}
		if c.ownedImages == nil {
			c.ownedImages = make(map[string]bool)
		}
		for _, path := range result.owned {
			c.ownedImages[path] = true
		}
		c.restoration = nil
		// A confirmed Main switch may have advanced the retained effect's
		// destination while its image bytes were being acquired. Keep current
		// semantic metadata and adopt only the copied draft payloads.
		effect.Restore, effect.ShellRestore = result.effect.Restore, result.effect.ShellRestore
		return effect, true
	}
	source := c.event.Attachment
	c.restoration = &nativeRestoredEffect{attachment: source, effect: effect}
	c.workers.Go(func() {
		result := copyRestoredEffect(c.ctx, effect)
		result.attachment = source
		select {
		case c.runtime.messages <- nativeClientMessage{owner: c, restoration: &result}:
		case <-c.ctx.Done():
			result.cleanup()
		}
	})
	return effect, false
}

func (c *nativeClient) restorationResult(result nativeRestoredEffect) {
	if result.attachment != c.event.Attachment {
		result.cleanup()
		if c.restoration != nil && c.restoration.attachment == result.attachment {
			c.restoration = nil
		}
		return
	}
	c.restoration = &result
	if result.err != nil {
		c.feedback.Set(fmt.Sprintf("Image restoration failed: %v · press a key to retry", result.err), true, time.Now())
	} else {
		c.applyEffects()
	}
	c.refresh(time.Now())
}
