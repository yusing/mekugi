package executionobservation

import (
	"context"
	jsonv1 "encoding/json"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
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
	if !input.Prewarm && !input.ApprovalReview && input.MetadataValid && input.Metadata.RequestKind == "turn" && input.Session != "" && input.Thread != "" && !input.Metadata.ActivityIdentityInvalid && (input.Metadata.ThreadID == "" || input.Metadata.ThreadID == input.Thread) {
		p.Control.ObserveCompactionContext(input.Thread, input.Metadata.ContextWindowID)
	}
	if t == nil && !input.Prewarm && !input.ApprovalReview && input.MetadataValid && input.Metadata.RequestKind == "turn" {
		// Execution-free turns retain native payloads but still publish generation
		// observations. They acquire no execution cache, capture or replay owners.
		t = &mekugiResponseTransform{owners: p, ctx: ctx, generationOnly: true,
			threadID:     p.admitActivityThread(input.Thread, input.Metadata),
			directory:    p.observationDirectory(input.Metadata.Directories, input.Thread),
			usageTracker: p.Usage.Observation(input.Thread, input.Metadata.ThreadID, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"]))}
	}
	if t == nil {
		return nil, nil
	}
	if input.MetadataValid && !t.generationOnly {
		observeReview(p.Review, t.directory, t.threadID, input.Metadata)
		if !input.Automatic {
			beginLiveDiffTurn(p.Review.Preview(), t.directory, t.threadID, input.Metadata)
		}
	}
	t.toolTrace = input.Trace
	return t, nil
}

// AdmitFork receives only confirmed host identity and cwd, before a child
// turn starts. Review membership supplies workspace identity, not permissions.
func (p *Preparer) AdmitFork(source, thread, workspace string) {
	if source == "" || thread == "" || source == thread || workspace == "" {
		return
	}
	directory := p.observationDirectory(map[string]jsonv1.RawMessage{workspace: nil}, source)
	if directory == "" {
		return
	}
	p.Review.IncludeThread(directory, thread, false)
}

func (t *mekugiResponseTransform) Usage() *accounting.Observation { return t.usageTracker }

func (t *mekugiResponseTransform) BindExecution(ctx context.Context) {
	if identity := t.ctx.Value(storageSessionKey{}); identity != nil {
		ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	}
	t.ctx = ctx
}
