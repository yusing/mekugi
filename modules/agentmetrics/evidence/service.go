// Package evidence measures observed operations without owning their execution.
package evidence

// Service exposes invocation-local observation and explicit offline inspection.
// Each operation owns its resources; the service retains no shared mutable state.
type Service struct{}
