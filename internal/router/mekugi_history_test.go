package router

import (
	"context"
	"strings"
)

// Helpers inspect the bounded cache in focused retention tests. Production recovery
// and confirmation consume only the isolated visible map on each transform.
func (p *mekugiProxy) reconcileInputPrefix(request *parsedResponsesRequest, sessionID string) error {
	workspace, _, _ := strings.Cut(sessionID, "\x00")
	_, err := p.reconcileVisibleInput(context.Background(), request, workspace, sessionID)
	return err
}
