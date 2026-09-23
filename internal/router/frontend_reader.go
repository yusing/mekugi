package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const codexThreadIDEnvironment = "CODEX_THREAD_ID"

// executeFrontendReader preserves reader AX evidence around one authenticated
// executable-frontend invocation. Codex remains the process owner.
func executeFrontendReader(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	args []string,
	contribution toolContribution,
) (execution toolplugin.ExecutionOutput, err error) {
	journal := manifest.AXReadOutput
	if journal == "" {
		journal = os.Getenv(capturer.AXReadOutputEnvironment)
	}
	observation, observeErr := capturer.StartAXReadWithContext(
		journal,
		os.Getenv(codexThreadIDEnvironment),
		contribution.Name,
		capturer.AXReadContext{},
	)
	var notices strings.Builder
	if observeErr != nil {
		fmt.Fprintf(&notices, "%s: AX read evidence unavailable: %v\n", contribution.Name, observeErr)
	}
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
		var exitCode *int
		if err == nil {
			exitCode = new(execution.ExitCode)
		}
		succeeded := err == nil && execution.ExitCode == 0 && ctx.Err() == nil
		if finishErr := observation.FinishResult(succeeded, class, exitCode); finishErr != nil {
			fmt.Fprintf(&notices, "%s: AX read evidence incomplete: %v\n", contribution.Name, finishErr)
		}
		execution.Stderr = notices.String() + execution.Stderr
	}()

	return toolplugin.Execute(
		ctx,
		manifest.NodeExecutable,
		runtimeRoot,
		contribution.Module,
		contribution.ModuleIndex,
		args,
		nil,
		"",
		nil,
	)
}
