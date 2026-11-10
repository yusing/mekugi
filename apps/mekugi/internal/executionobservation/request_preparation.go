package executionobservation

import (
	"context"
	jsonv1 "encoding/json"
	"errors"
	"fmt"

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
	var prefix *codexresponses.InputPrefix
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
	if t == nil && p.ThreadControl != nil {
		instructions, err := p.ThreadControl.RetainedInstructions(input.Thread, request.Model())
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
	if !input.Prewarm && !input.ApprovalReview && input.MetadataValid && input.Metadata.RequestKind == "turn" && input.Session != "" && input.Thread != "" && !input.Metadata.ActivityIdentityInvalid && (input.Metadata.ThreadID == "" || input.Metadata.ThreadID == input.Thread) {
		workspace := p.observationDirectory(input.Metadata.Directories, input.Thread)
		if t != nil {
			workspace = t.directory
		}
		p.Control.ObserveCompactionContext(input.Thread, input.Metadata.ContextWindowID)
		prefix = p.Control.InputPrefix(input.Session, input.Thread, workspace)
		if p.Registry != nil && p.Registry.Guidance != nil && p.Registry.Guidance.InitialContext != nil {
			err := prefix.Contribute(request, func() ([]jsonv1.RawMessage, error) {
				return p.Registry.Guidance.InitialContext(ctx, workspace)
			})
			if err != nil {
				if t != nil {
					t.Close()
				}
				return nil, err
			}
		}
	}
	if input.MetadataValid && input.Session != "" && input.Thread != "" && !input.Metadata.ActivityIdentityInvalid &&
		(input.Metadata.ThreadID == "" || input.Metadata.ThreadID == input.Thread) && p.Registry != nil && p.Registry.Guidance != nil && p.Registry.Guidance.ContextUsage != nil {
		admitted := binding.ContextUsage{Session: input.Session, Thread: input.Thread, FreshInput: input.FreshInput}
		if !input.Prewarm && !input.ApprovalReview && input.Metadata.RequestKind == "turn" && !input.Automatic && p.Inspect != nil {
			if details, err := p.Inspect(ctx, input.Thread); err == nil && details.ContextTokens != nil && details.ContextWindow != nil {
				admitted.Tokens, admitted.Window = *details.ContextTokens, *details.ContextWindow
				if details.Compactions != nil {
					admitted.Compactions = *details.Compactions
				}
			}
		}
		admitted.Notify = func(text string) {
			if p.Event != nil {
				p.Event(input.Session, input.Thread, fmt.Sprintf("context-usage:%d:%s", admitted.Compactions, text), text)
			}
		}
		if err := requestexecution.ProjectInstructions(request, func(text binding.InstructionInput) (binding.InstructionUpdate, error) {
			return p.Registry.Guidance.ContextUsage(text, admitted)
		}); err != nil {
			if t != nil {
				t.Close()
			}
			return nil, err
		}
	}
	if t == nil && !input.Prewarm && !input.ApprovalReview && input.MetadataValid && input.Metadata.RequestKind == "turn" {
		// Execution-free turns retain native payloads but still publish message
		// boundaries. They acquire no execution cache, capture or replay owners.
		t = &mekugiResponseTransform{owners: p, ctx: ctx, generationOnly: true,
			threadID:     p.admitActivityThread(input.Thread, input.Metadata),
			directory:    p.observationDirectory(input.Metadata.Directories, input.Thread),
			usageTracker: p.Usage.Observation(input.Thread, input.Metadata.ThreadID, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"]))}
	}
	if t == nil {
		return nil, nil
	}
	t.inputPrefix = prefix
	if err := t.checkCompactionCost(request); err != nil {
		t.Close()
		return nil, err
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
	p.Control.ForkInputPrefix(source, thread, directory)
}

func (t *mekugiResponseTransform) FreezeInputPrefix(request *codexresponses.Request) error {
	return t.inputPrefix.Freeze(request)
}

func (t *mekugiResponseTransform) Usage() *accounting.Observation { return t.usageTracker }

func (p *Preparer) projectThreadInstructions(request *codexresponses.Request, instructions []guidance.Replacement) error {
	if len(instructions) == 0 {
		return nil
	}
	if p.Registry.Guidance == nil || p.Registry.Guidance.Threads == nil {
		return errors.New("independent thread instruction projection is unavailable")
	}
	return requestexecution.ProjectInstructions(request, func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
		return p.Registry.Guidance.Threads(input, instructions)
	})
}

func (t *mekugiResponseTransform) BindExecution(ctx context.Context) {
	if identity := t.ctx.Value(storageSessionKey{}); identity != nil {
		ctx = context.WithValue(ctx, storageSessionKey{}, identity)
	}
	t.ctx = ctx
}
