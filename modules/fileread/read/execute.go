package read

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
)

// Execute reads source rows; the host publishes any returned recovery payloads.
func Execute(ctx context.Context, args []string, _ *os.File) (command.Output, error) {
	if err := ctx.Err(); err != nil {
		return command.Output{}, err
	}
	specs, options, parseErr := parse(args)
	if parseErr != nil {
		return command.Output{
			Stderr:       "mcat: " + parseErr.Error() + "\n",
			ExitCode:     1,
			FailureClass: "invalid_arguments",
		}, nil
	}
	budget := options.Budget
	if len(specs) == 1 {
		execution, err := scan(ctx, specs[0], options)
		if err == nil && execution.OmittedOutput != nil {
			start := readBundleStart(specs[0])
			shown, omitted := bundleRowCount(execution.Stdout), bundleRowCount(execution.OmittedOutput.Stdout)
			execution.OmittedOutput.SourceRow = start + shown
			shownStart := start
			if options.Tail {
				execution.OmittedOutput.SourceRow = start
				shownStart = start + omitted
			}
			if budget != 6000 {
				execution.OmittedOutput.MaxTokens = budget
			}
			var notices strings.Builder
			for line := range strings.Lines(execution.Stderr) {
				if !strings.HasPrefix(line, "mcat: output incomplete:") {
					notices.WriteString(line)
				}
			}
			fmt.Fprintf(&notices, "mcat: shown %s of %d rows (%d-token limit)\n", bundleRowSpan(shownStart, execution.Stdout), shown+omitted, budget)
			execution.Stderr = notices.String()
		}
		return execution, err
	}
	return executeBundle(ctx, specs, budget, options.Number)
}
