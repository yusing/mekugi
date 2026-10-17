// Source: apps/mekugi/internal/router/provider_diagnostics.go:1:33@b9679c93.
package requestexecution

import (
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func ProviderDiagnostics() codexresponses.Diagnostics {
	return codexresponses.Diagnostics{
		BufferBytes: codexresponses.MaxResponseBytes,
		Error: func(code, summary, detail string) error {
			if detail == "" {
				return StaticError(code, summary)
			}
			return &DiagnosticError{Cause: errors.New("provider reported a streaming error"), Code: code, Summary: summary, CallerDetail: detail, Distinct: true}
		},
		DescribeError: func(err error) (string, string, bool) {
			d, ok := errors.AsType[*DiagnosticError](err)
			if !ok {
				return "", "", false
			}
			message := d.Summary
			if d.CallerDetail != "" {
				message = d.CallerDetail
			}
			return d.Code, message, true
		},
		Incompatible: inferencegateway.IncompatibleRequest,
		ForwardError: ForwardError,
	}
}
