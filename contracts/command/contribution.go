package command

import (
	"context"
	"os"

	"github.com/yusing/mekugi/contracts/scope"
)

// Contribution pairs catalog metadata with its compiled command implementation.
// Run receives the exact host argv and stdin and returns streams and status to Codex.
type Contribution struct {
	Spec
	Run func(context.Context, Request) (Output, error)
}

// Request carries exact process input and the authenticated host-selected scope.
// Services are supplied by composition, never looked up through the request.
type Request struct {
	Args         []string
	Stdin        *os.File
	Scope        scope.Retained
	ProcessScope ProcessScope
}

// Definition binds a command to its module's constructed service.
type Definition[S any] struct {
	Spec
	Run func(S, context.Context, Request) (Output, error)
}
