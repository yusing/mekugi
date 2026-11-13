package executionobservation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// AdmitNativeChild borrows the invocation's resource owners. Native owns child
// identity, work and startup; this barrier only publishes retained resource scope.
func (p *Preparer) AdmitNativeChild(ctx context.Context, input appserver.ChildHostAdmissionRequest) error {
	if p == nil || p.Retained.Store == nil {
		return errors.New("native child admission storage is unavailable")
	}
	if strings.TrimSpace(input.RootThreadID) == "" || strings.TrimSpace(input.ParentThreadID) == "" || strings.TrimSpace(input.ThreadID) == "" ||
		input.ThreadID == input.ParentThreadID || input.ThreadID == input.RootThreadID {
		return errors.New("invalid native child admission identity")
	}
	if input.LocalCwd != nil && (!filepath.IsAbs(*input.LocalCwd) || filepath.Clean(*input.LocalCwd) != *input.LocalCwd) {
		return errors.New("invalid native child local workspace")
	}
	store := p.Retained.Store
	if err := store.Transaction(ctx, p.Retained.Writes, func(tx *history.Transaction) error {
		root, err := store.ReadAdmittedScope(input.RootThreadID)
		if err != nil {
			return err
		}
		parent, err := store.ReadAdmittedScope(input.ParentThreadID)
		if err != nil {
			return err
		}
		if parent.Namespace != root.Namespace {
			return errors.New("native child parent is outside the root handle scope")
		}
		return tx.InitializeScope(history.Scope{Thread: input.ThreadID, Namespace: parent.Namespace, Parent: input.ParentThreadID}, nil)
	}); err != nil {
		return err
	}
	// A remote-only child has no local workspace. Do not borrow its parent's cwd
	// or manufacture an execution window before the first actual request.
	if input.LocalCwd != nil {
		p.Review.IncludeThread(*input.LocalCwd, input.ThreadID, false)
	}
	return nil
}
