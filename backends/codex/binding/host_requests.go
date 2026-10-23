package binding

import (
	"fmt"
)

type DispatchOutcome string

const (
	NotDispatched DispatchOutcome = "not_dispatched"
	HostRejected  DispatchOutcome = "rejected"
	Uncertain     DispatchOutcome = "uncertain"
)

// DispatchError distinguishes an explicit host rejection from a lost
// acknowledgement. Uncertain effects must be reconciled before another dispatch.
type DispatchError struct {
	Outcome DispatchOutcome
	Method  string
	Cause   error
}

func (e *DispatchError) Error() string {
	return fmt.Sprintf("%s (%s): %v", e.Method, e.Outcome, e.Cause)
}
func (e *DispatchError) Unwrap() error { return e.Cause }
