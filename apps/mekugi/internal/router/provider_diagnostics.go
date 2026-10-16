package router

import (
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func providerCodecDiagnostics() codexresponses.Diagnostics {
	return codexresponses.Diagnostics{
		BufferBytes: codexresponses.MaxResponseBytes,
		Error: func(code, summary, detail string) error {
			if detail == "" {
				return staticCriticalDiagnostic(code, summary)
			}
			return &criticalDiagnosticError{err: errors.New("provider reported a streaming error"), code: code, summary: summary, callerDetail: detail, distinct: true}
		},
		DescribeError: func(err error) (string, string, bool) {
			d, ok := errors.AsType[*criticalDiagnosticError](err)
			if !ok {
				return "", "", false
			}
			message := d.summary
			if d.callerDetail != "" {
				message = d.callerDetail
			}
			return d.code, message, true
		},
		Incompatible: inferencegateway.IncompatibleRequest,
		ForwardError: forwardCriticalDiagnostic,
	}
}
