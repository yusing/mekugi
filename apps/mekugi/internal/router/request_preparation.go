package router

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func (p *mekugiProxy) Prepare(ctx context.Context, request *codexresponses.Request, input requestexecution.Preparation) (requestexecution.Prepared, error) {
	t, err := p.prepareModelRequest(ctx, request, input.Session, input.Thread, input.Metadata, input.MetadataValid, input.Prewarm)
	if errors.Is(err, binding.ErrUnsupportedExecutionInterface) {
		return nil, inferencegateway.IncompatibleRequest("unsupported_execution_interface", err.Error())
	}
	if err != nil || t == nil {
		return nil, err
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

func (t *mekugiResponseTransform) BindExecution(ctx context.Context) {
	if identity := t.ctx.Value(storageSessionKey{}); identity != nil {
		ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	}
	t.ctx = ctx
}
