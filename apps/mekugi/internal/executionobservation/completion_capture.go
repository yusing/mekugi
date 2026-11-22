package executionobservation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/outputbudget"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

// CompletionTokens bounds one automatic attachment, including source labels,
// diagnostics and its continuation. Explicit preloads and reloads remain full.
const CompletionTokens = 4000

type completionSource struct {
	command    string
	start, end int
}

// Frame only complete, visible diff rows. Leave the raw prefix and retained
// suffix in their original representation, including paging diagnostics.
func completionText(result compact.CapturedSource, sources []completionSource) string {
	var text strings.Builder
	var bodies []attachments.SourceRange
	through := 0
	for _, source := range sources {
		if source.start >= len(result.Stdout) {
			break
		}
		end := source.end
		if end > len(result.Stdout) {
			end = source.start + strings.LastIndexByte(result.Stdout[source.start:], '\n') + 1
		}
		if end <= source.start {
			break
		}
		text.WriteString(result.Stdout[through:source.start])
		fmt.Fprintf(&text, "Source %q:\n", source.command)
		start := text.Len()
		text.WriteString(result.Stdout[source.start:end])
		bodies = append(bodies, attachments.SourceRange{Start: start, End: text.Len()})
		through = end
	}
	if len(bodies) == 0 {
		return result.Text()
	}
	text.WriteString(result.Stdout[through:])
	result.Stdout = text.String()
	return attachments.SourceRanges(result.Text(), bodies)
}

func boundCompletion(ctx context.Context, result compact.CapturedSource, sources []completionSource, selected changes.Scope) (string, error) {
	budget := outputbudget.Budget{}
	// Avoid copying a full retained diff just to reject it on the byte limit.
	if len(result.Stdout) <= 4*CompletionTokens {
		text := completionText(result, sources)
		if fits, err := budget.Fits(text, CompletionTokens); err != nil || fits {
			return text, err
		}
	}
	if selected.Store == nil {
		return "", errors.New("completion retained storage is unavailable")
	}
	full := result.Stdout
	path, _, err := selected.Store.WriteReadOnlyFile(ctx, selected.Thread, selected.Limits, selected.Writes, "completion", ".txt", []byte(full))
	if err != nil {
		return "", err
	}
	result.Stderr = "Completion output exceeds budget. Read the complete output with: mcat " + commandargs.Quote(path) + "\n"
	trailerTokens, err := budget.Count("\n" + result.Stderr)
	if err != nil {
		return "", err
	}
	remaining := CompletionTokens - trailerTokens
	var text string
	for {
		prefix, err := budget.SelectText(full, remaining, false)
		if err != nil {
			return "", err
		}
		result.Stdout = prefix.Text
		text = completionText(result, sources)
		tokens, err := budget.Count(text)
		if err != nil {
			return "", err
		}
		if tokens <= CompletionTokens {
			break
		}
		remaining -= tokens - CompletionTokens
		if remaining <= 0 {
			return "", errors.New("completion continuation exceeds output budget")
		}
	}
	return text, nil
}
