// Source: apps/mekugi/internal/router/frontend_reader.go:1:73@[7b6d9ccc]
package frontends

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/contracts/command"
)

const codexThreadIDEnvironment = "CODEX_THREAD_ID"

// executeFrontendReader preserves reader AX evidence around one authenticated
// executable-frontend invocation. Codex remains the process owner.
func executeFrontendReader(
	ctx context.Context,
	metrics *evidence.Service,
	axReadOutput string,
	name string,
	request command.Request,
	run func(context.Context, command.Request) (command.Output, error),
) (execution command.Output, err error) {
	finish := beginFrontendRead(metrics, axReadOutput, name)
	defer func() {
		class := execution.FailureClass
		if err != nil {
			class = "execution_error"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "deadline_exceeded"
		} else if ctx.Err() != nil {
			class = "canceled"
		}
		finish(&execution, err, err == nil && execution.ExitCode == 0 && ctx.Err() == nil, class)
	}()

	return run(ctx, request)
}

// beginFrontendRead owns AX setup and publication, not reader outcome policy.
func beginFrontendRead(metrics *evidence.Service, log, name string) func(*command.Output, error, bool, string) {
	if log == "" {
		log = os.Getenv(evidence.AXReadOutputEnvironment)
	}
	observation, observeErr := metrics.StartRead(
		log,
		os.Getenv(codexThreadIDEnvironment),
		name,
		evidence.AXReadContext{},
	)
	var notices strings.Builder
	if observeErr != nil {
		fmt.Fprintf(&notices, "%s: AX read evidence unavailable: %v\n", name, observeErr)
	}
	return func(execution *command.Output, err error, succeeded bool, class string) {
		var exitCode *int
		if err == nil {
			exitCode = new(execution.ExitCode)
		}
		if finishErr := observation.FinishResult(succeeded, class, exitCode); finishErr != nil {
			fmt.Fprintf(&notices, "%s: AX read evidence incomplete: %v\n", name, finishErr)
		}
		execution.Stderr = notices.String() + execution.Stderr
	}
}
