// Source: apps/mekugi/internal/router/provider_diagnostics.go:1:33@b9679c93.
package requestexecution

import (
	"errors"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func ProviderDiagnostics() codexresponses.Diagnostics {
	return codexresponses.Diagnostics{
		Error: func(code, summary, detail string) error {
			if detail == "" {
				return StaticError(code, summary)
			}
			return &DiagnosticError{Cause: errors.New("provider reported a streaming error"), Code: code, Summary: summary, CallerDetail: detail, Distinct: true}
		},
	}
}
