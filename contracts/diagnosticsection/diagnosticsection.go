// Package diagnosticsection defines the optional evidence supplied to diagnostics.
package diagnosticsection

import (
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/contracts/inspection"
)

type Record struct {
	Kind     string
	Failure  bool
	Metadata map[string]jsontext.Value
}

// Transport interprets the capture owner's existing records and metrics.
type Transport struct {
	Record  func(map[string]jsontext.Value) Record
	Metrics func(map[string]jsontext.Value) map[string]jsontext.Value
}

// Reads is one fully validated read-log snapshot. An invalid source supplies no
// partial counts, but its selections can still describe unavailable read evidence.
type Reads interface {
	Threads() []string
	Anonymous() (jsontext.Value, error)
	Select(string) (jsontext.Value, inspection.AXCollector, error)
}

type Contribution struct {
	Transport    *Transport
	PrepareReads func(string, ...string) error
	ReadSnapshot func(context.Context, string) (Reads, error)
}

type Definition[S any] struct {
	Transport    *Transport
	PrepareReads func(S, string, ...string) error
	ReadSnapshot func(S, context.Context, string) (Reads, error)
}
