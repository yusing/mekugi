package executionobservation

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/guidance"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func (p *Preparer) Prepare(ctx context.Context, request *codexresponses.Request, input requestexecution.Preparation) (requestexecution.Prepared, error) {
	var t *mekugiResponseTransform
	var err error
	if input.ApprovalReview {
		if err := p.restoreApprovalReviewContext(ctx, request, input); err != nil {
			return nil, err
		}
	} else {
		t, err = p.prepareModelRequest(ctx, request, input.Session, input.Thread, input.Metadata, input.MetadataValid, input.Prewarm)
		if errors.Is(err, binding.ErrUnsupportedExecutionInterface) {
			return nil, inferencegateway.IncompatibleRequest("unsupported_execution_interface", err.Error())
		}
		if err != nil {
			return nil, err
		}
	}
	if t == nil && p.threadControl != nil {
		instructions, err := p.threadControl.RetainedInstructions(input.Thread, request.Model())
		if err == nil {
			err = p.projectThreadInstructions(request, instructions)
		}
		if err != nil {
			return nil, err
		}
	}
	projected, err := threadbinding.ProjectLocalIdentities(request.Fields["input"])
	if err != nil {
		if t != nil {
			t.Close()
		}
		return nil, err
	}
	request.Fields["input"] = projected
	if t == nil {
		return nil, nil
	}
	if input.MetadataValid {
		observeReview(p.review, t.directory, t.threadID, input.Metadata)
		if !input.Automatic {
			beginLiveDiffTurn(p.review.Preview(), t.directory, t.threadID, input.Metadata)
		}
	}
	t.toolTrace = input.Trace
	return t, nil
}

func (t *mekugiResponseTransform) Usage() *accounting.Observation { return t.usageTracker }

func (p *Preparer) projectThreadInstructions(request *codexresponses.Request, instructions []guidance.Replacement) error {
	if len(instructions) == 0 {
		return nil
	}
	if p.registry.Guidance == nil || p.registry.Guidance.Threads == nil {
		return errors.New("independent thread instruction projection is unavailable")
	}
	return requestexecution.ProjectInstructions(request, func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
		return p.registry.Guidance.Threads(input, instructions)
	})
}

func (t *mekugiResponseTransform) BindExecution(ctx context.Context) {
	if identity := t.ctx.Value(storageSessionKey{}); identity != nil {
		ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	}
	t.ctx = ctx
}
