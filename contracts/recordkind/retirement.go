// Package recordkind defines contributions from retained-record owners.
package recordkind

import "context"

// Retirement describes the exact proposed removals in one bounded batch.
// An owner must treat Deleted as read-only. Thread is empty for unowned records.
type Retirement struct {
	Deleted map[string]bool
	Thread  string
}

// RetirementPlanner prepares an owner's replacement bytes without performing
// storage effects. A nil update leaves the record unchanged. Preparing must not
// mutate the planner: a stale revision or a reader lease can reject the batch.
type RetirementPlanner func(context.Context, Retirement) (*RetirementUpdate, error)

// RetirementUpdate carries the replacement and the planner for its new value.
// The host adopts Next only after publishing Data successfully.
type RetirementUpdate struct {
	Data []byte
	Next RetirementPlanner
}
