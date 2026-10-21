package launch

import (
	"context"
	"os"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

type markdownLinkKey struct {
	scope  terminalsession.SourceContext
	target string
}

type markdownLinkResult struct {
	key    markdownLinkKey
	exists bool
}

// The event loop owns admissions. Workers receive detached targets and publish
// results without touching the renderer or the selected attachment.
type markdownLinkChecks struct {
	attachment uint64
	admitted   map[terminalsession.SourceContext]map[string]bool
	pending    map[markdownLinkKey]bool
	results    chan markdownLinkResult
}

func (c *markdownLinkChecks) selectAttachment(attachment uint64) {
	if c.attachment != attachment {
		clear(c.admitted)
		c.attachment = attachment
	}
}

func (c *markdownLinkChecks) known(scope terminalsession.SourceContext) map[string]bool {
	return c.admitted[scope]
}

func (c *markdownLinkChecks) request(ctx context.Context, scope terminalsession.SourceContext, targets []string) {
	c.selectAttachment(scope.Attachment)
	if c.results == nil {
		c.results = make(chan markdownLinkResult, 4)
		c.pending = make(map[markdownLinkKey]bool)
		c.admitted = make(map[terminalsession.SourceContext]map[string]bool)
	}
	for _, target := range targets {
		key := markdownLinkKey{scope, target}
		if _, known := c.admitted[scope][target]; known || c.pending[key] {
			continue
		}
		if len(c.pending) == cap(c.results) || ctx.Err() != nil {
			return // A completion repaints and requests remaining visible targets.
		}
		path, ok := markdownFileTarget(scope.Workspace, target)
		if !ok {
			c.finish(markdownLinkResult{key: key})
			continue
		}
		c.pending[key] = true
		results := c.results
		go func() {
			_, err := os.Stat(path)
			if os.IsNotExist(err) {
				if base, _, first, _ := markdownFileLocation(path); first > 0 {
					_, err = os.Stat(base)
				}
			}
			select {
			case results <- markdownLinkResult{key, err == nil}:
			case <-ctx.Done():
			}
		}()
	}
}

func (c *markdownLinkChecks) finish(result markdownLinkResult) {
	delete(c.pending, result.key)
	if result.key.scope.Attachment != c.attachment {
		return
	}
	known := c.admitted[result.key.scope]
	if known == nil {
		known = make(map[string]bool)
		c.admitted[result.key.scope] = known
	}
	known[result.key.target] = result.exists
}
