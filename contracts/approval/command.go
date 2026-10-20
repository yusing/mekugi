// Package approval identifies an exact expanded command grant.
package approval

// Command excludes display labels, thread selection, and secret answers.
// Arguments is the JSON encoding of the expanded argv, preserving boundaries.
type Command struct{ Directory, Executable, Arguments string }
