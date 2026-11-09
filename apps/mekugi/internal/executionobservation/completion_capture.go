package executionobservation

import (
	"context"
	"errors"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/outputbudget"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

// CompletionTokens bounds one automatic attachment, including source labels,
// diagnostics and its continuation. Explicit preloads and reloads remain full.
const CompletionTokens = 4000

// ReadCompletion freezes bounded attachment variants before injection. Filtered
// results keep a full fallback; each variant retains its omitted suffix separately.
func ReadCompletion(ctx context.Context, selection, directory string, changes compact.ChangeReader, retained *recovery.Session) (compact.Snapshot, error) {
	if changes.YieldContext == "" {
		changes.Yielded = nil
	}
	snapshot, err := readCompletion(ctx, selection, directory, changes, retained)
	if err != nil || len(changes.Yielded) == 0 {
		return snapshot, err
	}
	// Freeze both bounded variants before injection. A reset can occur while
	// delivery is queued or before the next inference hydrates its reference.
	changes.Yielded = nil
	full, err := readCompletion(ctx, selection, directory, changes, retained)
	if err != nil {
		return compact.Snapshot{}, err
	}
	snapshot.CompletionContext, snapshot.FullCompletion = changes.YieldContext, full.Completion
	return snapshot, nil
}

func readCompletion(ctx context.Context, selection, directory string, changes compact.ChangeReader, retained *recovery.Session) (compact.Snapshot, error) {
	if err := compact.CompletionSources(selection); err != nil {
		return compact.Snapshot{}, err
	}
	snapshot, err := compact.ReadSources(ctx, selection, directory, nil, changes)
	if err != nil {
		return compact.Snapshot{}, err
	}
	var body strings.Builder
	if len(changes.Yielded) != 0 {
		body.WriteString("Captures already reported by yield summaries omitted; explicit mchanges reads retain full diffs.\n")
	}
	result := &compact.CapturedSource{}
	for _, source := range snapshot.Sources {
		body.WriteString(source.Text())
		body.WriteByte('\n')
		result.ExitCode = max(result.ExitCode, source.ExitCode)
	}
	result.Stdout = body.String()
	snapshot.Sources, snapshot.Completion = nil, result
	budget := outputbudget.Budget{}
	fits, err := budget.Fits(result.Text(), CompletionTokens)
	if err != nil || fits {
		return snapshot, err
	}
	if retained == nil {
		return compact.Snapshot{}, errors.New("completion output recovery is unavailable")
	}
	refs, err := retained.Reserve(ctx, 1)
	if err != nil {
		return compact.Snapshot{}, err
	}
	result.Stderr = recovery.NextCall(refs[0], CompletionTokens)
	trailerTokens, err := budget.Count("\n" + result.Stderr)
	if err != nil {
		return compact.Snapshot{}, err
	}
	full := result.Stdout
	remaining := CompletionTokens - trailerTokens
	for {
		prefix, err := budget.SelectText(full, remaining, false)
		if err != nil {
			return compact.Snapshot{}, err
		}
		result.Stdout = prefix.Text
		tokens, err := budget.Count(result.Text())
		if err != nil {
			return compact.Snapshot{}, err
		}
		if tokens <= CompletionTokens {
			break
		}
		remaining -= tokens - CompletionTokens
		if remaining <= 0 {
			return compact.Snapshot{}, errors.New("completion continuation exceeds output budget")
		}
	}
	_, err = retained.PublishAt(ctx, refs[0], command.OmittedOutput{Stdout: full[len(result.Stdout):]}, result.ExitCode)
	if err != nil {
		return compact.Snapshot{}, err
	}
	return snapshot, nil
}
