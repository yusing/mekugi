package requestexecution

// Source: apps/mekugi/internal/router/server.go:465:485,553:567@[e5c6fadc] catalog diagnostics.
import (
	"errors"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

// ReportCatalog retains catalog failures without interrupting inference.
func (c *Failures) ReportCatalog(request *http.Request, failure inferencegateway.CatalogFailure) {
	err := failure.Err
	if failure.Code == "" {
		err = modelsForwardDiagnostic(err)
	} else if err == nil {
		err = StaticError(failure.Code, failure.Summary)
	} else {
		err = Classify(err, failure.Code, failure.Summary, true)
	}
	result := c.Report(Failure{Session: request.Header.Get(codexresponses.RoutingSessionHeader), Thread: codexresponses.ThreadID(request.Header), Phase: "models", UpstreamStatus: failure.UpstreamStatus}, err)
	debug, requestID := DebugRequest(request.Context())
	if debug != nil {
		debug.Event(map[string]any{
			"event": "models_request_failure", "request_id": requestID,
			"session_id": request.Header.Get(codexresponses.RoutingSessionHeader), "thread_id": codexresponses.ThreadID(request.Header),
			"upstream_status": failure.UpstreamStatus, "downstream_status": failure.DownstreamStatus,
			"diagnostic_code": result.Code, "diagnostic_reference": result.Reference, "error": result.Error,
		})
	}
}

func modelsForwardDiagnostic(err error) error {
	if _, invalid := errors.AsType[*inferencegateway.ModelCredentialsError](err); invalid {
		return Classify(err, "models_auth_headers", "the model catalog request is missing valid Codex Authorization or account headers", true)
	}
	if _, classified := errors.AsType[*DiagnosticError](err); classified {
		return err
	}
	diagnostic, _ := errors.AsType[*DiagnosticError](ForwardError(err))
	code := diagnostic.Code
	if !strings.HasPrefix(code, "models_") {
		code = "models_" + code
	}
	return &DiagnosticError{Cause: err, Code: code,
		Summary: "the model catalog could not be fetched: " + diagnostic.Summary, Distinct: true}
}
