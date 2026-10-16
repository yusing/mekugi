// Source: apps/mekugi/internal/router/frontend_recovery.go:35:85@[7b6d9ccc]
package frontends

import (
	"context"
	"errors"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func retainExecutionOutput(ctx context.Context, service *recovery.Service, selected scope.Retained, execution command.Output) (command.Output, error) {
	if execution.OmittedOutput == nil && execution.Delivery == nil {
		return execution, nil
	}
	store, err := service.Attach(selected, recovery.DefaultLimits(), nil)
	if err != nil {
		return execution, err
	}
	if execution.Delivery != nil {
		plan := execution.Delivery
		execution.Delivery = nil
		handles, err := store.Reserve(ctx, plan.ReferenceCount())
		if err != nil {
			return execution, err
		}
		prepared, err := plan.Prepare(handles)
		if err != nil {
			return execution, err
		}
		for _, output := range prepared.Recovery {
			if output.ReferenceIndex < 0 || output.ReferenceIndex >= len(handles) {
				return prepared.Unreferenced, errors.New("recovery reference outside reserved slots")
			}
			id := handles[output.ReferenceIndex]
			if len(output.ChangeSelection) != 0 {
				if output.OmittedOutput != (command.OmittedOutput{}) || output.ExitCode != 0 {
					return prepared.Unreferenced, errors.New("change selection cannot contain omitted output")
				}
				_, err = store.PublishSelectionAt(ctx, id, output.ChangeSelection)
			} else {
				_, err = store.PublishAt(ctx, id, output.OmittedOutput, output.ExitCode)
			}
			if err != nil {
				return prepared.Unreferenced, err
			}
		}
		return prepared.Referenced, nil
	}
	id, err := store.Publish(ctx, *execution.OmittedOutput, execution.ExitCode)
	if err != nil {
		return execution, err
	}
	omitted := execution.OmittedOutput
	execution.OmittedOutput = nil
	if omitted.SourceRow != 0 {
		execution.Stderr = strings.TrimSuffix(execution.Stderr, "\n") + "; " + strings.TrimPrefix(recovery.NextCall(id, omitted.MaxTokens), "read: incomplete; ")
	} else {
		execution.Stderr += recovery.NextCall(id)
	}
	return execution, nil
}
