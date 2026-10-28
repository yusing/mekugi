package launch

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Inspect borrows live facts when available, otherwise reads the admitted source.
// Neither path admits a runtime or starts work.
func (e *threadExecution) Inspect(ctx context.Context, thread string) (control.SessionDetails, error) {
	member, ok := e.shared.Member(thread)
	if !ok {
		return control.SessionDetails{}, control.ErrNotAdmitted
	}
	if runtime, err := e.Runtime(thread); err == nil {
		if details, err := runtime.Inspect(ctx, thread); err == nil {
			return details, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return control.SessionDetails{}, err
	}
	if strings.HasPrefix(member.Settings.Model, "claude:") {
		details, err := sessioncontrol.ReadClaudeDetails(ctx, thread, member.Path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return details, err
	}
	observed := rollout.ReadObservations(ctx, thread, member.Path)
	if err := ctx.Err(); err != nil {
		return control.SessionDetails{}, err
	}
	details := control.SessionDetails{Model: observed.Model, ReasoningEffort: observed.Effort, ServiceTier: observed.Tier,
		SessionMetrics: control.SessionMetrics{ContextTokens: observed.ContextTokens, ContextWindow: observed.Window, Compactions: observed.Compactions}}
	if details.ServiceTier != nil {
		details.ServiceTier = new((*inferencegateway.Tiers)(nil).Effective("", *details.ServiceTier, thread))
	}
	return details, nil
}
