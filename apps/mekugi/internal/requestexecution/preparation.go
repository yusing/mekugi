package requestexecution

import (
	"context"
	"net/http"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// Preparation identifies one admitted request, including automatic continuation.
type Preparation struct {
	Session, Thread                   string
	Metadata                          codexresponses.TurnMetadata
	MetadataValid, Prewarm, Automatic bool
	Trace                             Trace
}

// Preparer joins execution observations and review admission before forwarding.
// Implementations keep capture and retained-publication state at their owners.
type Preparer interface {
	Prepare(context.Context, *codexresponses.Request, Preparation) (Prepared, error)
}

// Prepared has request lifetime. Rebinding drops only the response-start deadline.
type Prepared interface {
	codexresponses.Transformer
	Usage() *accounting.Observation
	ObserveUsage(inference.Usage)
	BindExecution(context.Context)
	Close()
}

// Naming is the request-facing part of the invocation's naming coordinator.
type Naming interface {
	ObserveInput(string, []byte)
	Completed(string, http.Header)
}
