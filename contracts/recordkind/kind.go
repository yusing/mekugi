package recordkind

import "context"

// Record is an owner's retained encoded value. Name is a validated leaf identity.
type Record struct {
	Name string
	Data []byte
}

// Spec declares recognition and semantic inspection of one retained record kind.
// Nil Dependencies means the kind has no shared retained dependencies.
type Spec struct {
	ID           string
	Match        func(string) bool
	Dependencies func(Record) ([]string, error)
	Retirement   func(context.Context, Record) (RetirementPlanner, error)
	Keep         bool
	// Limits apply to this owner's records. SeparateBudget excludes its bytes
	// from the shared store budget; Limit bounds all records of this kind.
	SeparateBudget bool
	Limit          int64
	MaxRecordBytes int64
}
