package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const codexThreadIDEnvironment = "CODEX_THREAD_ID"

// executeFrontendReader preserves reader AX evidence around one authenticated
// executable-frontend invocation. Codex remains the process owner.
func executeFrontendReader(
	ctx context.Context,
	manifest toolWorkerManifest,
	args []string,
	contribution toolContribution,
) (execution command.Output, err error) {
	finish := beginFrontendRead(manifest, contribution.Name)
	defer func() {
		class := frontendExecutionFailure(execution, err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "deadline_exceeded"
		} else if ctx.Err() != nil {
			class = "canceled"
		}
		finish(&execution, err, err == nil && execution.ExitCode == 0 && ctx.Err() == nil, class)
	}()

	return toolplugin.ExecuteBuiltin(ctx, contribution.Name, args)
}

func frontendExecutionFailure(execution command.Output, err error) string {
	if err != nil {
		return "execution_error"
	}
	return execution.FailureClass
}

// beginFrontendRead owns AX setup and publication, not reader outcome policy.
func beginFrontendRead(manifest toolWorkerManifest, name string) func(*command.Output, error, bool, string) {
	log := manifest.AXReadOutput
	if log == "" {
		log = os.Getenv(capturer.AXReadOutputEnvironment)
	}
	observation, observeErr := capturer.StartAXReadWithContext(
		log,
		os.Getenv(codexThreadIDEnvironment),
		name,
		capturer.AXReadContext{},
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
