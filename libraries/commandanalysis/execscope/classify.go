// Package execscope derives bounded write scopes without executing the user's program.
package execscope

import "time"

const ListingLimit = 4096

// Input carries the execution metadata selected by the request.
type Input struct {
	Command      string
	Directory    string
	Shell        string
	Deadline     time.Time
	Changes      ChangeResolver
	AuthoredOnly bool
}

// Classify keeps unknown effects distinct from known write operands.
func Classify(input Input) Result {
	return classifyExecShellSource(input.Command, input.Directory, input.Shell, input.Deadline, 0, input.Changes, input.AuthoredOnly)
}
