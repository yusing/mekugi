package reference

import (
	"context"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
)

// View borrows the current publication boundary. Retain may only be called
// during Resolve; a resolver must not start another publication transaction.
type View struct {
	Scope  scope.Retained
	Retain func(names ...string) error
}

// Resolver owns the meaning and canonical encoding of an evidence selection.
// Field is its existing record member, currently "changes".
type Resolver struct {
	Field        string
	Canonicalize func(data []byte, strict bool) ([]byte, error)
	Resolve      func(context.Context, View, []byte) (command.OmittedOutput, error)
	Dependencies func(scope.Retained, []byte) ([]string, error)
}
