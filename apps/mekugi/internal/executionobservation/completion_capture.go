package executionobservation

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/outputbudget"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

// CompletionTokens bounds one automatic attachment, including source labels,
// diagnostics and its continuation. Explicit preloads and reloads remain full.
const CompletionTokens = 4000

func boundCompletion(ctx context.Context, result *compact.CapturedSource, retained *recovery.Session) error {
	budget := outputbudget.Budget{}
	fits, err := budget.Fits(result.Text(), CompletionTokens)
	if err != nil || fits {
		return err
	}
	if retained == nil {
		return errors.New("completion output recovery is unavailable")
	}
	refs, err := retained.Reserve(ctx, 1)
	if err != nil {
		return err
	}
	result.Stderr = recovery.NextCall(refs[0], CompletionTokens)
	trailerTokens, err := budget.Count("\n" + result.Stderr)
	if err != nil {
		return err
	}
	full := result.Stdout
	remaining := CompletionTokens - trailerTokens
	for {
		prefix, err := budget.SelectText(full, remaining, false)
		if err != nil {
			return err
		}
		result.Stdout = prefix.Text
		tokens, err := budget.Count(result.Text())
		if err != nil {
			return err
		}
		if tokens <= CompletionTokens {
			break
		}
		remaining -= tokens - CompletionTokens
		if remaining <= 0 {
			return errors.New("completion continuation exceeds output budget")
		}
	}
	_, err = retained.PublishAt(ctx, refs[0], command.OmittedOutput{Stdout: full[len(result.Stdout):]}, result.ExitCode)
	return err
}
