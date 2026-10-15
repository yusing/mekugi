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
	ReferenceIndex int
	OmittedOutput
	ExitCode int
}
