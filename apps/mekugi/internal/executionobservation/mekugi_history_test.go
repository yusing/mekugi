package executionobservation

import (
	"context"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

// Helpers inspect the bounded cache in focused retention tests. Production recovery
// and confirmation consume only the isolated visible map on each transform.
func (p *Preparer) reconcileInputPrefix(request *codexresponses.Request, sessionID string) error {
	workspace, _, _ := strings.Cut(sessionID, "\x00")
	_, err := p.reconcileVisibleInput(context.Background(), request, workspace, sessionID)
	return err
}
