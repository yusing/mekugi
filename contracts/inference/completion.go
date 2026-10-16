// Package inference describes model exchanges and measured usage without agent control.
package inference

import "context"

type Request struct {
	Model, Effort, Instructions, Input string
}

// Complete returns completed assistant text. The adapter owns credentials,
// transport, terminal validation, and authoritative usage accounting.
type Complete func(context.Context, Request) (string, error)
