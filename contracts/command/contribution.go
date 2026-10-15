package command

import (
	"context"
	"os"
)

// Contribution pairs catalog metadata with its compiled command implementation.
// Run receives the exact host argv and stdin. The frontend host publishes any
// omitted output before returning streams and status to Codex.
type Contribution struct {
	Spec
	Run func(context.Context, []string, *os.File) (Output, error)
}
