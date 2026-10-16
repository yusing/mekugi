// Package inspection defines optional contributions to offline session queries.
package inspection

import (
	"context"
	"encoding/json/jsontext"
	"time"
)

type Event struct {
	Kind, TurnID, ItemID, CallID string
	At, Started, Completed       time.Time
	PersistedTimes               bool
	ExitCode                     *int
}

type Edit struct {
	CallID, Script      string
	Failed, Unconfirmed bool
}

type AXOptions struct {
	ThreadID, ReadLog, Defects string
	UnmatchedCalls             uint64
}

// AXCollector owns one query's pairing and report encoding.
type AXCollector interface {
	Observe(Event) error
	ObserveEdit(Edit)
	Report(context.Context, AXOptions) (jsontext.Value, error)
}

type UsageFilter struct {
	Since, Until        time.Time
	Model, ExcludeModel string
	Threads             map[string]bool
}

// Usage carries completeness counts and an owner-encoded token section.
type Usage struct {
	UnknownCompletenessRecords uint64         `json:"unknown_completeness_records"`
	State                      string         `json:"state"`
	ObservedRecords            uint64         `json:"observed_records"`
	MissingRecords             uint64         `json:"missing_records"`
	Tokens                     jsontext.Value `json:"tokens,omitempty"`
}

type Contribution struct {
	NewAX         func() AXCollector
	ProviderUsage func(context.Context, string, UsageFilter) (map[string]Usage, error)
}

type Definition[S any] struct {
	NewAX         func(S) AXCollector
	ProviderUsage func(S, context.Context, string, UsageFilter) (map[string]Usage, error)
}
