package command

// PendingDelivery finalizes captured output with actual reserved reference
// identities. Preparation performs no I/O and does not mutate the plan.
type PendingDelivery interface {
	ReferenceCount() int
	Prepare(references []string) (PreparedDelivery, error)
}

// PreparedDelivery keeps the same admitted bodies on publication failure.
// Neither output contains another plan or an unprocessed omission.
type PreparedDelivery struct {
	Referenced, Unreferenced Output
	Recovery                 []RecoveryOutput
}

// RecoveryOutput binds one captured payload to a reserved reference slot.
type RecoveryOutput struct {
	// ChangeSelection is an opaque retained-evidence selection. When set, all
	// output fields and ExitCode must be zero.
	ChangeSelection []byte
	ReferenceIndex  int
	OmittedOutput
	ExitCode int
}

// RecoveryFailure optionally supplies a command-specific result after recovery
// fails. It performs no I/O and must return neither Delivery nor OmittedOutput.
// Cancellation takes precedence. Completed mutations use it to preserve their
// full report and original status without repeating an effect.
type RecoveryFailure interface {
	RecoveryFailed(error) Output
}
